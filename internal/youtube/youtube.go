package youtube

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Segment represents a timed transcript segment.
type Segment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// SubtitleTrack represents one subtitle format entry from yt-dlp JSON.
type SubtitleTrack struct {
	Ext string `json:"ext"`
	URL string `json:"url"`
}

// Meta holds YouTube video metadata and available subtitle tracks.
type Meta struct {
	Title             string                     `json:"title"`
	Channel           string                     `json:"channel"`
	Duration          float64                    `json:"duration"`
	Thumbnail         string                     `json:"thumbnail"`
	Language          string                     `json:"language,omitempty"`
	Subtitles         map[string][]SubtitleTrack `json:"-"`
	AutomaticCaptions map[string][]SubtitleTrack `json:"-"`
}

// FindBinary locates an external CLI tool in PATH or common macOS/Homebrew/Python locations.
func FindBinary(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	candidates := []string{
		filepath.Join("/opt/homebrew/bin", name),
		filepath.Join("/usr/local/bin", name),
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".local", "bin", name))
		if matches, err := filepath.Glob(filepath.Join(home, "Library", "Python", "*", "bin", name)); err == nil {
			candidates = append(candidates, matches...)
		}
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	return ""
}

func cookieArgs(cookiesBrowser, cookiesFile string) []string {
	if cf := strings.TrimSpace(cookiesFile); cf != "" {
		if _, err := os.Stat(cf); err == nil {
			return []string{"--cookies", cf}
		}
	}
	b := strings.TrimSpace(strings.ToLower(cookiesBrowser))
	if b == "" || b == "none" {
		return nil
	}
	return []string{"--cookies-from-browser", b}
}

func extractYtdlpError(stderrText string) string {
	lines := strings.Split(stderrText, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "ERROR:") {
			return line
		}
	}
	trimmed := strings.TrimSpace(stderrText)
	if len(trimmed) > 300 {
		trimmed = trimmed[len(trimmed)-300:]
	}
	return trimmed
}

// FetchMeta retrieves video metadata via yt-dlp without downloading the video.
func FetchMeta(ctx context.Context, ytdlpBin, url, cookiesBrowser, cookiesFile string) (Meta, error) {
	args := []string{
		"--dump-single-json",
		"--no-playlist",
		"--skip-download",
		"--no-warnings",
	}
	args = append(args, cookieArgs(cookiesBrowser, cookiesFile)...)
	args = append(args, url)

	cmd := exec.CommandContext(ctx, ytdlpBin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		detail := extractYtdlpError(stderr.String())
		if detail != "" {
			return Meta{}, fmt.Errorf("yt-dlp: %s", detail)
		}
		return Meta{}, fmt.Errorf("yt-dlp meta error: %w", err)
	}
	var raw struct {
		Title             string                     `json:"title"`
		Channel           string                     `json:"channel"`
		Uploader          string                     `json:"uploader"`
		Duration          float64                    `json:"duration"`
		Thumbnail         string                     `json:"thumbnail"`
		Language          string                     `json:"language"`
		Subtitles         map[string][]SubtitleTrack `json:"subtitles"`
		AutomaticCaptions map[string][]SubtitleTrack `json:"automatic_captions"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return Meta{}, fmt.Errorf("parse meta json: %w", err)
	}
	ch := raw.Channel
	if ch == "" {
		ch = raw.Uploader
	}
	return Meta{
		Title:             raw.Title,
		Channel:           ch,
		Duration:          raw.Duration,
		Thumbnail:         raw.Thumbnail,
		Language:          raw.Language,
		Subtitles:         raw.Subtitles,
		AutomaticCaptions: raw.AutomaticCaptions,
	}, nil
}

func pickVTTURL(tracks map[string][]SubtitleTrack, preferred []string) string {
	if len(tracks) == 0 {
		return ""
	}
	findVTT := func(list []SubtitleTrack) string {
		for _, t := range list {
			if t.Ext == "vtt" && t.URL != "" {
				return t.URL
			}
		}
		return ""
	}
	for _, lang := range preferred {
		if lang == "" {
			continue
		}
		if u := findVTT(tracks[lang]); u != "" {
			return u
		}
		for k, list := range tracks {
			if strings.HasPrefix(k, lang+"-") || strings.HasPrefix(k, lang+"_") {
				if u := findVTT(list); u != "" {
					return u
				}
			}
		}
	}
	// Prefer original auto-caption track (*-orig) before falling back to any track.
	for k, list := range tracks {
		if strings.HasSuffix(k, "-orig") {
			if u := findVTT(list); u != "" {
				return u
			}
		}
	}
	for k, list := range tracks {
		if k == "live_chat" {
			continue
		}
		if u := findVTT(list); u != "" {
			return u
		}
	}
	return ""
}

// FetchSubtitleFromMeta downloads and parses VTT subtitles directly from the URLs
// already returned by FetchMeta, avoiding a second yt-dlp invocation.
func FetchSubtitleFromMeta(ctx context.Context, meta Meta, lang string) []Segment {
	var preferred []string
	if lang != "" && lang != "auto" {
		preferred = append(preferred, lang)
	}
	if meta.Language != "" {
		preferred = append(preferred, meta.Language)
	}
	preferred = append(preferred, "ru", "en")

	subURL := pickVTTURL(meta.Subtitles, preferred)
	if subURL == "" {
		subURL = pickVTTURL(meta.AutomaticCaptions, preferred)
	}
	if subURL == "" {
		return nil
	}

	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, subURL, nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	segs := ParseVTT(string(body))
	if len(segs) == 0 {
		return nil
	}
	return segs
}

// ExtractSubtitles tries to download manual or auto-generated VTT subtitles via yt-dlp.
// Returns nil, nil if no subtitles are available.
func ExtractSubtitles(ctx context.Context, ytdlpBin, url, lang, cookiesBrowser, cookiesFile, workDir string) ([]Segment, error) {
	subCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	subLangs := "en.*,ru.*,.*-orig"
	if lang != "" && lang != "auto" {
		subLangs = lang + ".*,en.*,ru.*"
	}
	outTemplate := filepath.Join(workDir, "subs.%(ext)s")
	args := []string{
		"--skip-download",
		"--no-playlist",
		"--socket-timeout", "15",
		"--retries", "2",
		"--write-subs",
		"--write-auto-subs",
		"--sub-format", "vtt",
		"--sub-langs", subLangs,
		"--no-warnings",
		"-o", outTemplate,
	}
	args = append(args, cookieArgs(cookiesBrowser, cookiesFile)...)
	args = append(args, url)
	cmd := exec.CommandContext(subCtx, ytdlpBin, args...)
	_ = cmd.Run() // yt-dlp may exit non-zero if some requested langs are missing while writing others

	matches, err := filepath.Glob(filepath.Join(workDir, "subs.*.vtt"))
	if err != nil || len(matches) == 0 {
		return nil, nil
	}

	// Prefer the explicitly requested language if present.
	chosen := matches[0]
	if lang != "" && lang != "auto" {
		for _, m := range matches {
			if strings.Contains(filepath.Base(m), "."+lang+".") {
				chosen = m
				break
			}
		}
	}

	data, err := os.ReadFile(chosen)
	if err != nil {
		return nil, err
	}
	segs := ParseVTT(string(data))
	if len(segs) == 0 {
		return nil, nil
	}
	return segs, nil
}

var (
	vttTimeRe = regexp.MustCompile(`^(?:(\d+):)?(\d{2}):(\d{2})[.,](\d{3})\s*-->\s*(?:(\d+):)?(\d{2}):(\d{2})[.,](\d{3})`)
	tagRe     = regexp.MustCompile(`<[^>]+>`)
	dlPctRe   = regexp.MustCompile(`\[download\]\s+(\d+(?:\.\d+)?)%`)
)

func parseTimestamp(h, m, s, ms string) float64 {
	var hh, mm, ss, mms int
	if h != "" {
		hh, _ = strconv.Atoi(h)
	}
	mm, _ = strconv.Atoi(m)
	ss, _ = strconv.Atoi(s)
	mms, _ = strconv.Atoi(ms)
	return float64(hh*3600+mm*60+ss) + float64(mms)/1000.0
}

// ParseVTT parses WebVTT content into deduplicated timed segments.
func ParseVTT(content string) []Segment {
	var segments []Segment
	scanner := bufio.NewScanner(strings.NewReader(content))

	var inCue bool
	var start, end float64
	var lines []string
	var lastText string

	flush := func() {
		if !inCue || len(lines) == 0 {
			inCue = false
			lines = nil
			return
		}
		joined := strings.TrimSpace(strings.Join(lines, " "))
		joined = html.UnescapeString(tagRe.ReplaceAllString(joined, ""))
		joined = strings.Join(strings.Fields(joined), " ")
		if joined != "" && joined != lastText {
			// YouTube auto-captions often repeat the previous cue as the first line of the next cue.
			if lastText != "" && strings.HasPrefix(joined, lastText+" ") {
				trimmed := strings.TrimSpace(strings.TrimPrefix(joined, lastText))
				if trimmed != "" {
					joined = trimmed
				}
			}
			segments = append(segments, Segment{
				Start: start,
				End:   end,
				Text:  joined,
			})
			lastText = joined
		}
		inCue = false
		lines = nil
	}

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if m := vttTimeRe.FindStringSubmatch(line); m != nil {
			flush()
			start = parseTimestamp(m[1], m[2], m[3], m[4])
			end = parseTimestamp(m[5], m[6], m[7], m[8])
			inCue = true
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if inCue {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	flush()
	return segments
}

// DownloadAudio downloads the best audio stream and converts it with ffmpeg.
// If forAPI is true, encodes compact 64kbps mono MP3; otherwise 16kHz mono PCM WAV for whisper-cli.
func DownloadAudio(ctx context.Context, ytdlpBin, ffmpegBin, url, cookiesBrowser, cookiesFile, workDir string, forAPI bool, onProgress func(pct int), onStage func(msg string)) (string, error) {
	rawPattern := filepath.Join(workDir, "raw_audio.%(ext)s")
	args := []string{
		"-f", "bestaudio/best",
		"--no-playlist",
		"--concurrent-fragments", "4",
		"--socket-timeout", "15",
		"--retries", "3",
		"--newline",
		"--no-warnings",
		"-o", rawPattern,
	}
	args = append(args, cookieArgs(cookiesBrowser, cookiesFile)...)
	args = append(args, url)
	cmd := exec.CommandContext(ctx, ytdlpBin, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("yt-dlp start: %w", err)
	}

	var tail []string
	scanner := bufio.NewScanner(stdout)
	lastPct := -1
	for scanner.Scan() {
		line := scanner.Text()
		tail = append(tail, line)
		if len(tail) > 25 {
			tail = tail[1:]
		}
		if m := dlPctRe.FindStringSubmatch(line); m != nil {
			if f, err := strconv.ParseFloat(m[1], 64); err == nil && onProgress != nil {
				pct := int(f)
				if pct != lastPct {
					lastPct = pct
					onProgress(pct)
				}
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		detail := extractYtdlpError(strings.Join(tail, "\n"))
		if detail != "" {
			return "", fmt.Errorf("yt-dlp: %s", detail)
		}
		return "", fmt.Errorf("yt-dlp audio download error: %w", err)
	}

	matches, err := filepath.Glob(filepath.Join(workDir, "raw_audio.*"))
	if err != nil || len(matches) == 0 {
		return "", fmt.Errorf("downloaded audio file not found")
	}
	rawPath := matches[0]

	var outPath string
	var ffArgs []string
	if onStage != nil {
		onStage("Converting audio with ffmpeg...")
	}
	if forAPI {
		outPath = filepath.Join(workDir, "audio.mp3")
		ffArgs = []string{"-y", "-i", rawPath, "-vn", "-ar", "16000", "-ac", "1", "-b:a", "64k", outPath}
	} else {
		outPath = filepath.Join(workDir, "audio.wav")
		ffArgs = []string{"-y", "-i", rawPath, "-vn", "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", outPath}
	}

	ffCmd := exec.CommandContext(ctx, ffmpegBin, ffArgs...)
	if out, err := ffCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ffmpeg error: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return outPath, nil
}

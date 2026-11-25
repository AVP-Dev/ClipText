package transcribe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"cliptext/internal/youtube"
)

// Result holds the completed transcription output.
type Result struct {
	Source      string            `json:"source"` // subtitles | local_whisper | api
	Text        string            `json:"text"`
	CleanedText string            `json:"cleaned_text,omitempty"`
	Segments    []youtube.Segment `json:"segments"`
}

var whisperProgressRe = regexp.MustCompile(`progress\s*=\s*(\d+)%`)

// JoinSegments builds plain readable text from timed segments.
func JoinSegments(segs []youtube.Segment) string {
	var parts []string
	for _, s := range segs {
		t := strings.TrimSpace(s.Text)
		if t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

// LocalWhisper runs whisper-cli on a 16kHz WAV file and parses its JSON output.
func LocalWhisper(ctx context.Context, whisperBin, modelPath, wavPath, lang, workDir string, onProgress func(pct int)) (Result, error) {
	if modelPath == "" {
		return Result{}, fmt.Errorf("no local Whisper model selected")
	}
	if _, err := os.Stat(modelPath); err != nil {
		return Result{}, fmt.Errorf("model file not found: %s", modelPath)
	}

	outPrefix := filepath.Join(workDir, "whisper_out")
	args := []string{
		"-m", modelPath,
		"-f", wavPath,
		"-oj",
		"-of", outPrefix,
		"-pp",
	}
	if lang != "" && lang != "auto" {
		args = append(args, "-l", lang)
	} else {
		args = append(args, "-l", "auto")
	}

	cmd := exec.CommandContext(ctx, whisperBin, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, err
	}
	cmd.Stdout = cmd.Stderr

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("failed to start whisper-cli: %w", err)
	}

	var tail []string
	scanner := bufio.NewScanner(stderr)
	lastPct := -1
	for scanner.Scan() {
		line := scanner.Text()
		tail = append(tail, line)
		if len(tail) > 20 {
			tail = tail[1:]
		}
		if m := whisperProgressRe.FindStringSubmatch(line); m != nil {
			if pct, err := strconv.Atoi(m[1]); err == nil && onProgress != nil {
				if pct != lastPct {
					lastPct = pct
					onProgress(pct)
				}
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		return Result{}, fmt.Errorf("whisper-cli failed: %w (%s)", err, strings.Join(tail, " | "))
	}

	jsonPath := outPrefix + ".json"
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return Result{}, fmt.Errorf("failed to read whisper-cli output: %w", err)
	}

	var parsed struct {
		Transcription []struct {
			Offsets struct {
				From int64 `json:"from"`
				To   int64 `json:"to"`
			} `json:"offsets"`
			Text string `json:"text"`
		} `json:"transcription"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Result{}, fmt.Errorf("failed to parse whisper-cli JSON: %w", err)
	}

	segs := make([]youtube.Segment, 0, len(parsed.Transcription))
	for _, item := range parsed.Transcription {
		txt := strings.TrimSpace(item.Text)
		if txt == "" {
			continue
		}
		segs = append(segs, youtube.Segment{
			Start: float64(item.Offsets.From) / 1000.0,
			End:   float64(item.Offsets.To) / 1000.0,
			Text:  txt,
		})
	}

	return Result{
		Source:   "local_whisper",
		Text:     JoinSegments(segs),
		Segments: segs,
	}, nil
}

// OpenAICompatible sends audio to an OpenAI-compatible /audio/transcriptions endpoint
// (supports Groq, OpenAI, NVIDIA NIM, and custom servers).
func OpenAICompatible(ctx context.Context, baseURL, apiKey, model, audioPath, lang string) (Result, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return Result{}, fmt.Errorf("API Base URL is required")
	}
	if model == "" {
		return Result{}, fmt.Errorf("API model is required")
	}

	res, status, err := callTranscriptionAPI(ctx, baseURL, apiKey, model, audioPath, lang, "verbose_json")
	if err != nil && (status == http.StatusBadRequest || status == http.StatusUnprocessableEntity) {
		// Some OpenAI-compatible endpoints only support response_format=json.
		res, _, err = callTranscriptionAPI(ctx, baseURL, apiKey, model, audioPath, lang, "json")
	}
	return res, err
}

func callTranscriptionAPI(ctx context.Context, baseURL, apiKey, model, audioPath, lang, responseFormat string) (Result, int, error) {
	f, err := os.Open(audioPath)
	if err != nil {
		return Result{}, 0, err
	}
	defer f.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filepath.Base(audioPath))
	if err != nil {
		return Result{}, 0, err
	}
	if _, err := io.Copy(part, f); err != nil {
		return Result{}, 0, err
	}

	_ = writer.WriteField("model", model)
	_ = writer.WriteField("response_format", responseFormat)
	if lang != "" && lang != "auto" {
		_ = writer.WriteField("language", lang)
	}
	if err := writer.Close(); err != nil {
		return Result{}, 0, err
	}

	endpoint := baseURL + "/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return Result{}, 0, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Result{}, 0, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, resp.StatusCode, fmt.Errorf("API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
	}

	var parsed struct {
		Text     string `json:"text"`
		Segments []struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
			Text  string  `json:"text"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return Result{}, resp.StatusCode, fmt.Errorf("failed to parse API response: %w", err)
	}

	segs := make([]youtube.Segment, 0, len(parsed.Segments))
	for _, s := range parsed.Segments {
		t := strings.TrimSpace(s.Text)
		if t != "" {
			segs = append(segs, youtube.Segment{
				Start: s.Start,
				End:   s.End,
				Text:  t,
			})
		}
	}
	text := strings.TrimSpace(parsed.Text)
	if text == "" && len(segs) > 0 {
		text = JoinSegments(segs)
	}
	if len(segs) == 0 && text != "" {
		segs = []youtube.Segment{{Start: 0, End: 0, Text: text}}
	}

	return Result{
		Source:   "api",
		Text:     text,
		Segments: segs,
	}, resp.StatusCode, nil
}

// FormatWithLLM sends transcript text to an OpenAI-compatible /chat/completions endpoint
// for punctuation cleanup or summarization.
func FormatWithLLM(ctx context.Context, baseURL, apiKey, model, text, instruction string) (string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || model == "" {
		return "", fmt.Errorf("LLM Base URL and model are required")
	}

	sysPrompt := "You are a video transcript editor. Turn the raw transcript into a clean, readable script: fix punctuation, correct obvious speech-to-text errors using context, split into logical paragraphs, and remove filler words. Preserve the original language and do not invent anything."
	if instruction == "summary" {
		sysPrompt = "You are a content analyst. Create a structured, concise summary of this video transcript in its original language: main topic, bulleted key takeaways, and core conclusions."
	}

	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": sysPrompt},
			{"role": "user", "content": text},
		},
		"temperature": 0.2,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("LLM API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBytes, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("LLM returned an empty response")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

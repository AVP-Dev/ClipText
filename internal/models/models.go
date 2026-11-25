package models

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ModelInfo describes either a locally discovered or downloadable whisper.cpp model.
type ModelInfo struct {
	Name        string `json:"name"`
	Path        string `json:"path,omitempty"`
	SizeBytes   int64  `json:"size_bytes"`
	Downloaded  bool   `json:"downloaded"`
	Description string `json:"description"`
	URL         string `json:"url,omitempty"`
}

type catalogEntry struct {
	Name        string
	SizeBytes   int64
	Description string
	URL         string
}

var catalog = []catalogEntry{
	{
		Name:        "ggml-large-v3-turbo-q5_0.bin",
		SizeBytes:   574_041_195,
		Description: "Best speed/quality balance (Q5_0, ~574 MB)",
		URL:         "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo-q5_0.bin",
	},
	{
		Name:        "ggml-large-v3-turbo.bin",
		SizeBytes:   1_624_555_275,
		Description: "Maximum turbo quality (F16, ~1.6 GB)",
		URL:         "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo.bin",
	},
	{
		Name:        "ggml-small.bin",
		SizeBytes:   487_601_967,
		Description: "Good accuracy, fast inference (~488 MB)",
		URL:         "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.bin",
	},
	{
		Name:        "ggml-base.bin",
		SizeBytes:   147_951_465,
		Description: "Lightweight base model (~148 MB)",
		URL:         "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.bin",
	},
	{
		Name:        "ggml-tiny.bin",
		SizeBytes:   77_691_713,
		Description: "Ultra-fast compact model (~78 MB)",
		URL:         "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-tiny.bin",
	},
}

// DefaultModelDir returns the primary directory where models are stored/downloaded.
func DefaultModelDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "models"
	}
	return filepath.Join(home, ".cache", "whisper.cpp")
}

func searchDirs() []string {
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs,
			filepath.Join(home, ".cache", "whisper.cpp"),
			filepath.Join(home, ".cache", "whisper"),
			filepath.Join(home, "Library", "Application Support", "whisper"),
			filepath.Join(home, "Models"),
		)
	}
	dirs = append(dirs,
		"/opt/homebrew/share/whisper-cpp",
		"/usr/local/share/whisper-cpp",
		"models",
	)
	return dirs
}

func isWhisperASRModel(name string) bool {
	lower := strings.ToLower(name)
	if !strings.HasPrefix(lower, "ggml-") || !strings.HasSuffix(lower, ".bin") {
		return false
	}
	if strings.Contains(lower, "silero") || strings.Contains(lower, "vad") {
		return false
	}
	return true
}

// List scans known directories for local models and merges them with the downloadable catalog.
func List() []ModelInfo {
	found := make(map[string]ModelInfo)

	for _, dir := range searchDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !isWhisperASRModel(entry.Name()) {
				continue
			}
			fullPath := filepath.Join(dir, entry.Name())
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if _, exists := found[entry.Name()]; !exists {
				found[entry.Name()] = ModelInfo{
					Name:        entry.Name(),
					Path:        fullPath,
					SizeBytes:   info.Size(),
					Downloaded:  true,
					Description: "Found locally",
				}
			}
		}
	}

	var result []ModelInfo
	seen := make(map[string]bool)

	for _, c := range catalog {
		seen[c.Name] = true
		if local, ok := found[c.Name]; ok {
			local.Description = c.Description
			result = append(result, local)
		} else {
			result = append(result, ModelInfo{
				Name:        c.Name,
				SizeBytes:   c.SizeBytes,
				Downloaded:  false,
				Description: c.Description,
				URL:         c.URL,
			})
		}
	}

	// Include any custom/extra local models not in the built-in catalog.
	var extras []ModelInfo
	for name, local := range found {
		if !seen[name] {
			extras = append(extras, local)
		}
	}
	sort.Slice(extras, func(i, j int) bool {
		return extras[i].Name < extras[j].Name
	})
	result = append(extras, result...)

	return result
}

// Download fetches a model from the catalog into DefaultModelDir() atomically.
func Download(ctx context.Context, filename string, onProgress func(percent int, downloaded, total int64)) (ModelInfo, error) {
	var target *catalogEntry
	for i := range catalog {
		if catalog[i].Name == filename {
			target = &catalog[i]
			break
		}
	}
	if target == nil {
		return ModelInfo{}, fmt.Errorf("unknown model: %s", filename)
	}

	dir := DefaultModelDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ModelInfo{}, fmt.Errorf("failed to create models directory: %w", err)
	}

	destPath := filepath.Join(dir, target.Name)
	tmpPath := destPath + ".tmp"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL, nil)
	if err != nil {
		return ModelInfo{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ModelInfo{}, fmt.Errorf("download error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ModelInfo{}, fmt.Errorf("HTTP %d while downloading model", resp.StatusCode)
	}

	total := resp.ContentLength
	if total <= 0 {
		total = target.SizeBytes
	}

	f, err := os.Create(tmpPath)
	if err != nil {
		return ModelInfo{}, err
	}
	defer func() {
		f.Close()
		_ = os.Remove(tmpPath)
	}()

	buf := make([]byte, 64*1024)
	var downloaded int64
	lastPct := -1

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := f.Write(buf[:n]); wErr != nil {
				return ModelInfo{}, wErr
			}
			downloaded += int64(n)
			if total > 0 && onProgress != nil {
				pct := int(downloaded * 100 / total)
				if pct > 100 {
					pct = 100
				}
				if pct != lastPct {
					lastPct = pct
					onProgress(pct, downloaded, total)
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return ModelInfo{}, readErr
		}
	}

	if err := f.Close(); err != nil {
		return ModelInfo{}, err
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return ModelInfo{}, err
	}

	return ModelInfo{
		Name:        target.Name,
		Path:        destPath,
		SizeBytes:   downloaded,
		Downloaded:  true,
		Description: target.Description,
	}, nil
}

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config holds user-configurable settings persisted locally.
type Config struct {
	Mode           string `json:"mode"`            // auto | subtitles | local | api
	Language       string `json:"language"`        // auto | ru | en | ...
	CookiesBrowser string `json:"cookies_browser"` // "" | chrome | safari | firefox | brave | arc | edge
	CookiesFile    string `json:"cookies_file"`    // optional path to Netscape cookies.txt
	LocalModel     string `json:"local_model"`     // path to ggml-*.bin

	APIPreset  string `json:"api_preset"`   // groq | nvidia | openai | custom
	APIBaseURL string `json:"api_base_url"` // e.g. https://api.groq.com/openai/v1
	APIKey     string `json:"api_key"`
	APIModel   string `json:"api_model"`

	LLMEnabled bool   `json:"llm_enabled"`
	LLMBaseURL string `json:"llm_base_url"`
	LLMAPIKey  string `json:"llm_api_key"`
	LLMModel   string `json:"llm_model"`
}

// Default returns sensible defaults for a local-first setup.
func Default() Config {
	return Config{
		Mode:           "auto",
		Language:       "auto",
		CookiesBrowser: "chrome",
		APIPreset:      "groq",
		APIBaseURL: "https://api.groq.com/openai/v1",
		APIModel:   "whisper-large-v3-turbo",
		LLMBaseURL: "https://api.groq.com/openai/v1",
		LLMModel:   "llama-3.3-70b-versatile",
	}
}

func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "cliptext.json", nil
	}
	return filepath.Join(dir, "cliptext", "config.json"), nil
}

// Load reads config from disk, returning Default() if none exists yet.
func Load() Config {
	cfg := Default()
	path, err := configPath()
	if err != nil {
		return cfg
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	if cfg.Mode == "" {
		cfg.Mode = "auto"
	}
	if cfg.Language == "" {
		cfg.Language = "auto"
	}
	return cfg
}

// Save writes config to disk with 0600 permissions (protects API keys).
func Save(cfg Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

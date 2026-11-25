package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"cliptext/internal/config"
	"cliptext/internal/models"
	"cliptext/internal/transcribe"
	"cliptext/internal/youtube"
)

// Server serves the embedded web UI and REST/SSE endpoints.
type Server struct {
	indexHTML []byte
	mux       *http.ServeMux
}

// New creates a configured HTTP handler with CORS support for browser extensions.
func New(indexHTML []byte) http.Handler {
	s := &Server{
		indexHTML: indexHTML,
		mux:       http.NewServeMux(),
	}
	s.routes()
	return withCORS(s.mux)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /", s.handleIndex)
	s.mux.HandleFunc("GET /api/system", s.handleSystem)
	s.mux.HandleFunc("POST /api/config", s.handleSaveConfig)
	s.mux.HandleFunc("POST /api/cookies", s.handleSaveCookies)
	s.mux.HandleFunc("POST /api/models/download", s.handleModelDownload)
	s.mux.HandleFunc("POST /api/transcribe", s.handleTranscribe)
	s.mux.HandleFunc("POST /api/format", s.handleFormat)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(s.indexHTML)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	cfg := config.Load()
	modelList := models.List()

	// Auto-select first downloaded local model if none configured yet.
	if cfg.LocalModel == "" {
		for _, m := range modelList {
			if m.Downloaded {
				cfg.LocalModel = m.Path
				break
			}
		}
	}
	if cfg.CookiesFile == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			p := filepath.Join(dir, "cliptext", "cookies.txt")
			if _, err := os.Stat(p); err == nil {
				cfg.CookiesFile = p
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tools": map[string]bool{
			"yt_dlp":      youtube.FindBinary("yt-dlp") != "",
			"ffmpeg":      youtube.FindBinary("ffmpeg") != "",
			"whisper_cli": youtube.FindBinary("whisper-cli") != "",
		},
		"models": modelList,
		"config": cfg,
	})
}

func (s *Server) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	var cfg config.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := config.Save(cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSaveCookies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	} else {
		dir = filepath.Join(dir, "cliptext")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	cookiePath := filepath.Join(dir, "cookies.txt")
	if err := os.WriteFile(cookiePath, []byte(req.Content), 0o600); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": cookiePath})
}

type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	mu      sync.Mutex
}

func newSSEWriter(w http.ResponseWriter) (*sseWriter, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	return &sseWriter{w: w, flusher: flusher}, true
}

func (s *sseWriter) send(event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, data)
	s.flusher.Flush()
}

func (s *Server) handleModelDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filename string `json:"filename"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Filename == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model filename is required"})
		return
	}

	sse, ok := newSSEWriter(w)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	info, err := models.Download(r.Context(), req.Filename, func(pct int, downloaded, total int64) {
		sse.send("progress", map[string]any{
			"percent":          pct,
			"downloaded_bytes": downloaded,
			"total_bytes":      total,
		})
	})
	if err != nil {
		sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	sse.send("done", map[string]any{"model": info})
}

type transcribeRequest struct {
	URL            string `json:"url"`
	Mode           string `json:"mode"`
	Language       string `json:"language"`
	CookiesBrowser string `json:"cookies_browser"`
	CookiesFile    string `json:"cookies_file"`
	LocalModel     string `json:"local_model"`
	APIBaseURL     string `json:"api_base_url"`
	APIKey         string `json:"api_key"`
	APIModel       string `json:"api_model"`
	LLMEnabled     bool   `json:"llm_enabled"`
	LLMBaseURL     string `json:"llm_base_url"`
	LLMAPIKey      string `json:"llm_api_key"`
	LLMModel       string `json:"llm_model"`
}

func (s *Server) handleTranscribe(w http.ResponseWriter, r *http.Request) {
	var req transcribeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "please enter a YouTube URL"})
		return
	}

	ytdlpBin := youtube.FindBinary("yt-dlp")
	if ytdlpBin == "" {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "yt-dlp not found on system"})
		return
	}

	sse, ok := newSSEWriter(w)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	workDir, err := os.MkdirTemp("", "cliptext-*")
	if err != nil {
		sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	defer os.RemoveAll(workDir)

	ctx := r.Context()

	sse.send("status", map[string]string{"step": "meta", "message": "Fetching video metadata..."})
	sse.send("progress", map[string]any{"percent": 5, "message": "Fetching metadata..."})

	meta, err := youtube.FetchMeta(ctx, ytdlpBin, req.URL, req.CookiesBrowser, req.CookiesFile)
	if err != nil {
		sse.send("error", map[string]string{"message": err.Error()})
		return
	}
	sse.send("meta", meta)

	var result transcribe.Result

	// Step 1: Try subtitles if mode is "auto" or "subtitles".
	if req.Mode == "" || req.Mode == "auto" || req.Mode == "subtitles" {
		sse.send("status", map[string]string{"step": "subtitles", "message": "Checking available subtitles..."})
		sse.send("progress", map[string]any{"percent": 20, "message": "Searching subtitles..."})

		segs := youtube.FetchSubtitleFromMeta(ctx, meta, req.Language)
		if len(segs) == 0 {
			var subErr error
			segs, subErr = youtube.ExtractSubtitles(ctx, ytdlpBin, req.URL, req.Language, req.CookiesBrowser, req.CookiesFile, workDir)
			if subErr != nil {
				segs = nil
			}
		}
		if len(segs) > 0 {
			result = transcribe.Result{
				Source:   "subtitles",
				Text:     transcribe.JoinSegments(segs),
				Segments: segs,
			}
		} else if req.Mode == "subtitles" {
			sse.send("error", map[string]string{"message": "No subtitles available for this video. Switch to Auto or Whisper mode."})
			return
		}
	}

	// Step 2: Audio download + transcription if subtitles were not used.
	if result.Text == "" {
		ffmpegBin := youtube.FindBinary("ffmpeg")
		if ffmpegBin == "" {
			sse.send("error", map[string]string{"message": "ffmpeg not found on system"})
			return
		}

		useAPI := req.Mode == "api"
		if req.Mode == "auto" && req.LocalModel == "" && req.APIKey != "" {
			useAPI = true
		}

		sse.send("status", map[string]string{"step": "audio", "message": "Downloading audio stream..."})
		sse.send("progress", map[string]any{"percent": 22, "message": "Connecting to audio stream..."})
		audioPath, err := youtube.DownloadAudio(ctx, ytdlpBin, ffmpegBin, req.URL, req.CookiesBrowser, req.CookiesFile, workDir, useAPI, func(pct int) {
			scaled := 22 + pct*26/100
			sse.send("progress", map[string]any{"percent": scaled, "message": fmt.Sprintf("Downloading audio: %d%%", pct)})
		}, func(msg string) {
			sse.send("progress", map[string]any{"percent": 48, "message": msg})
		})
		if err != nil {
			sse.send("error", map[string]string{"message": err.Error()})
			return
		}

		if useAPI {
			sse.send("status", map[string]string{"step": "transcribe", "message": "Sending audio to API..."})
			sse.send("progress", map[string]any{"percent": 65, "message": "Transcribing via API..."})

			result, err = transcribe.OpenAICompatible(ctx, req.APIBaseURL, req.APIKey, req.APIModel, audioPath, req.Language)
			if err != nil {
				sse.send("error", map[string]string{"message": err.Error()})
				return
			}
		} else {
			whisperBin := youtube.FindBinary("whisper-cli")
			if whisperBin == "" {
				sse.send("error", map[string]string{"message": "whisper-cli not found on system"})
				return
			}
			modelPath := req.LocalModel
			if modelPath == "" {
				for _, m := range models.List() {
					if m.Downloaded {
						modelPath = m.Path
						break
					}
				}
			}
			sse.send("status", map[string]string{"step": "transcribe", "message": "Transcribing locally (Whisper)..."})
			result, err = transcribe.LocalWhisper(ctx, whisperBin, modelPath, audioPath, req.Language, workDir, func(pct int) {
				scaled := 50 + pct*45/100
				sse.send("progress", map[string]any{"percent": scaled, "message": fmt.Sprintf("Whisper: %d%%", pct)})
			})
			if err != nil {
				sse.send("error", map[string]string{"message": err.Error()})
				return
			}
		}
	}

	// Step 3: Optional LLM cleanup.
	if req.LLMEnabled && req.LLMBaseURL != "" && req.LLMModel != "" {
		sse.send("status", map[string]string{"step": "cleanup", "message": "Cleaning and formatting text via LLM..."})
		sse.send("progress", map[string]any{"percent": 95, "message": "LLM post-processing..."})
		cleaned, err := transcribe.FormatWithLLM(ctx, req.LLMBaseURL, req.LLMAPIKey, req.LLMModel, result.Text, "clean")
		if err == nil && cleaned != "" {
			result.CleanedText = cleaned
		}
	}

	sse.send("progress", map[string]any{"percent": 100, "message": "Done!"})
	sse.send("result", result)
}

func (s *Server) handleFormat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text        string `json:"text"`
		Instruction string `json:"instruction"`
		LLMBaseURL  string `json:"llm_base_url"`
		LLMAPIKey   string `json:"llm_api_key"`
		LLMModel    string `json:"llm_model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty text"})
		return
	}
	out, err := transcribe.FormatWithLLM(r.Context(), req.LLMBaseURL, req.LLMAPIKey, req.LLMModel, req.Text, req.Instruction)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": out})
}

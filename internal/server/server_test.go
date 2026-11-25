package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleIndexAndSystem(t *testing.T) {
	h := New([]byte("<html><body>ClipText</body></html>"))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "<html><body>ClipText</body></html>" {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}

	reqSys := httptest.NewRequest(http.MethodGet, "/api/system", nil)
	recSys := httptest.NewRecorder()
	h.ServeHTTP(recSys, reqSys)
	if recSys.Code != http.StatusOK {
		t.Fatalf("GET /api/system status = %d, want 200", recSys.Code)
	}

	var payload struct {
		Tools  map[string]bool `json:"tools"`
		Models []any           `json:"models"`
		Config map[string]any  `json:"config"`
	}
	if err := json.Unmarshal(recSys.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON from /api/system: %v", err)
	}
	if _, ok := payload.Tools["yt_dlp"]; !ok {
		t.Errorf("missing yt_dlp in tools")
	}
	if len(payload.Models) == 0 {
		t.Errorf("expected non-empty model catalog")
	}
}

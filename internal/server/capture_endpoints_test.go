package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureEndpointsAreOperatorOnly(t *testing.T) {
	corpusDir := t.TempDir()
	s := New(&Config{
		CallerPort:     8080,
		OperatorPort:   8081,
		BaseURL:        "http://localhost:8080",
		SpecDir:        "../../spec",
		CaptureEnabled: true,
		CorpusDir:      corpusDir,
	})

	// Capture one entry so both status and save responses prove they report
	// the middleware's current state rather than just its configuration.
	wrapped := s.captureMiddleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/test", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("capture setup request: expected status %d, got %d", http.StatusNoContent, w.Code)
	}

	t.Run("status on operator mux", func(t *testing.T) {
		resp := serveMuxRequest(s.operatorMux, http.MethodGet, "/_seam/capture/status")
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
		}

		var status struct {
			Enabled    bool   `json:"enabled"`
			EntryCount int    `json:"entry_count"`
			CorpusDir  string `json:"corpus_dir"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			t.Fatalf("decode status response: %v", err)
		}
		if !status.Enabled || status.EntryCount != 1 || status.CorpusDir != corpusDir {
			t.Errorf("unexpected status response: %+v", status)
		}
	})

	t.Run("save on operator mux", func(t *testing.T) {
		resp := serveMuxRequest(s.operatorMux, http.MethodPost, "/_seam/capture/save")
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
		}

		var result struct {
			Status     string `json:"status"`
			EntryCount int    `json:"entry_count"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode save response: %v", err)
		}
		if result.Status != "saved" || result.EntryCount != 1 {
			t.Errorf("unexpected save response: %+v", result)
		}

		data, err := os.ReadFile(filepath.Join(corpusDir, "corpus.json"))
		if err != nil {
			t.Fatalf("read corpus after save: %v", err)
		}
		var corpus CorpusFile
		if err := json.Unmarshal(data, &corpus); err != nil {
			t.Fatalf("decode corpus after save: %v", err)
		}
		if len(corpus.Entries) != 1 {
			t.Fatalf("expected one saved corpus entry, got %d", len(corpus.Entries))
		}
	})

	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "status is not exposed on caller mux", method: http.MethodGet, path: "/_seam/capture/status"},
		{name: "save is not exposed on caller mux", method: http.MethodPost, path: "/_seam/capture/save"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := serveMuxRequest(s.callerMux, tc.method, tc.path)
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("expected caller mux status %d, got %d", http.StatusNotFound, resp.StatusCode)
			}
		})
	}
}

func TestCaptureEndpointsMethodAndStateErrors(t *testing.T) {
	t.Run("wrong methods", func(t *testing.T) {
		s := New(&Config{
			CallerPort:   8080,
			OperatorPort: 8081,
			BaseURL:      "http://localhost:8080",
			SpecDir:      "../../spec",
		})

		for _, tc := range []struct {
			name   string
			method string
			path   string
		}{
			{name: "status rejects POST", method: http.MethodPost, path: "/_seam/capture/status"},
			{name: "save rejects GET", method: http.MethodGet, path: "/_seam/capture/save"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				resp := serveMuxRequest(s.operatorMux, tc.method, tc.path)
				defer func() { _ = resp.Body.Close() }()
				if resp.StatusCode != http.StatusMethodNotAllowed {
					t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, resp.StatusCode)
				}
			})
		}
	})

	t.Run("status reports disabled capture", func(t *testing.T) {
		s := New(&Config{
			CallerPort:   8080,
			OperatorPort: 8081,
			BaseURL:      "http://localhost:8080",
			SpecDir:      "../../spec",
		})

		resp := serveMuxRequest(s.operatorMux, http.MethodGet, "/_seam/capture/status")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
		}

		var status map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			t.Fatalf("decode status response: %v", err)
		}
		if status["enabled"] != false || status["entry_count"] != float64(0) || status["corpus_dir"] != "" {
			t.Errorf("unexpected disabled status response: %+v", status)
		}
	})

	t.Run("save rejects disabled capture", func(t *testing.T) {
		s := New(&Config{
			CallerPort:   8080,
			OperatorPort: 8081,
			BaseURL:      "http://localhost:8080",
			SpecDir:      "../../spec",
		})

		resp := serveMuxRequest(s.operatorMux, http.MethodPost, "/_seam/capture/save")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, resp.StatusCode)
		}
	})

	t.Run("save reports persistence failure", func(t *testing.T) {
		root := t.TempDir()
		corpusPath := filepath.Join(root, "corpus")
		if err := os.WriteFile(corpusPath, []byte("not a directory"), 0o600); err != nil {
			t.Fatalf("create invalid corpus path: %v", err)
		}
		s := New(&Config{
			CallerPort:     8080,
			OperatorPort:   8081,
			BaseURL:        "http://localhost:8080",
			SpecDir:        "../../spec",
			CaptureEnabled: true,
			CorpusDir:      corpusPath,
		})

		resp := serveMuxRequest(s.operatorMux, http.MethodPost, "/_seam/capture/save")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, resp.StatusCode)
		}
	})
}

func serveMuxRequest(mux http.Handler, method, path string) *http.Response {
	req := httptest.NewRequest(method, path, nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, req)
	return recorder.Result()
}

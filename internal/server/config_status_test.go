package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfigStatusReportsRuntimeStateWithoutSecrets(t *testing.T) {
	s := New(&Config{
		CallerPort:    8080,
		OperatorPort:  8081,
		BaseURL:       "https://operator:REPLACE@example.test:8443?token=REPLACE",
		SpecDir:       "../../spec",
		AllowlistFile: newBaselineAllowlistFile(t),
	})

	req := httptest.NewRequest(http.MethodGet, "/config/status", nil)
	recorder := httptest.NewRecorder()
	s.configStatusHandler(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("expected JSON content type, got %q", contentType)
	}

	var status map[string]interface{}
	if err := json.NewDecoder(recorder.Body).Decode(&status); err != nil {
		t.Fatalf("decode /config/status response: %v", err)
	}

	for _, section := range []string{"config", "spec", "routes", "corpus", "health"} {
		if status[section] == nil {
			t.Errorf("missing %q section", section)
		}
	}

	config := status["config"].(map[string]interface{})
	if got := config["base_url"]; got != "https://example.test:8443" {
		t.Errorf("base_url was not redacted safely: got %v", got)
	}

	specStatus := status["spec"].(map[string]interface{})
	if hash, ok := specStatus["hash"].(string); !ok || len(hash) != 64 {
		t.Errorf("expected full spec hash, got %v", specStatus["hash"])
	}

	routes := status["routes"].(map[string]interface{})
	if count, ok := routes["enabled_count"].(float64); !ok || count == 0 {
		t.Errorf("expected enabled route count, got %v", routes["enabled_count"])
	}

	corpus := status["corpus"].(map[string]interface{})
	if enabled, ok := corpus["enabled"].(bool); !ok || enabled {
		t.Errorf("expected capture corpus to be disabled, got %v", corpus["enabled"])
	}

	health := status["health"].(map[string]interface{})
	if health["status"] != "healthy" {
		t.Errorf("expected healthy runtime status, got %v", health["status"])
	}
	if strings.Contains(recorder.Body.String(), "REPLACE") {
		t.Error("runtime status response exposed redacted URL data")
	}
}

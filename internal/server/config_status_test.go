package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestConfigStatusSurfacesEnvCredentialOwnerQuarantine(t *testing.T) {
	root := t.TempDir()
	ownerDir := filepath.Join(root, "owner")
	if err := os.MkdirAll(ownerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fragment := `openapi: 3.1.0
info:
  title: env credential status test
  version: "1"
x-seam-schema: v1
x-seam-owner: owner
x-upstream: https://api.example.com
x-credential-source: env
x-credential-env: SEAM_CRED_OTHER_API_KEY
x-inject-as:
  kind: bearer
paths:
  /test:
    get:
      responses:
        "200":
          description: OK
`
	if err := os.WriteFile(filepath.Join(ownerDir, "route.yaml"), []byte(fragment), 0o600); err != nil {
		t.Fatal(err)
	}

	s := New(&Config{
		CallerPort:    8080,
		OperatorPort:  8081,
		BaseURL:       "http://localhost:8080",
		SpecDir:       "../../spec",
		SchemaPath:    "../../spec/route-fragment-schema.json",
		FragmentMode:  true,
		FragmentsDir:  root,
		AllowlistFile: newBaselineAllowlistFile(t),
	})
	recorder := httptest.NewRecorder()
	s.configStatusHandler(recorder, httptest.NewRequest(http.MethodGet, "/config/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("/config/status status = %d", recorder.Code)
	}
	var status map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode /config/status: %v", err)
	}
	if got := status["quarantined_count"]; got != float64(1) {
		t.Fatalf("quarantined_count = %v, want 1", got)
	}
	fragments, ok := status["fragments"].([]interface{})
	if !ok || len(fragments) != 1 {
		t.Fatalf("/config/status fragments = %v, want one quarantine record", status["fragments"])
	}
	fragmentStatus, ok := fragments[0].(map[string]interface{})
	if !ok || fragmentStatus["status"] != "quarantined" {
		t.Fatalf("fragment status = %v, want quarantined", fragments[0])
	}
	reasons, ok := fragmentStatus["quarantine_reasons"].([]interface{})
	if !ok || len(reasons) == 0 || !strings.Contains(reasons[0].(string), "credential_env_ownership_failed") {
		t.Fatalf("quarantine reasons = %v, want env ownership rejection", fragmentStatus["quarantine_reasons"])
	}
}

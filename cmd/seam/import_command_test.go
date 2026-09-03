package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	seamspec "github.com/ardenone/seam/internal/spec"
)

func TestRunImportCommandMissingURL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runImportCommand([]string{}, &stdout, &stderr)

	// Should return error when URL is missing
	if code != 2 {
		t.Errorf("Expected return code 2 for missing URL, got %d: stderr=%s", code, stderr.String())
	}

	if !strings.Contains(stderr.String(), "--from-url is required") {
		t.Errorf("Expected error about missing --from-url, got: %s", stderr.String())
	}
}

func TestRunImportCommandInvalidURL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runImportCommand([]string{
		"--from-url", "not-a-valid-url",
	}, &stdout, &stderr)

	// Should return error for invalid URL
	if code != 2 {
		t.Errorf("Expected return code 2 for invalid URL, got %d: stderr=%s", code, stderr.String())
	}
}

func TestRunImportCommandValidFetch(t *testing.T) {
	// Create a test server that serves a minimal OpenAPI spec
	spec := `{
  "openapi": "3.1.0",
  "info": {
    "title": "Test API",
    "version": "1.0.0"
  },
  "paths": {
    "/api/test": {
      "get": {
        "summary": "Test endpoint",
        "responses": {
          "200": {
            "description": "Success"
          }
        }
      }
    }
  }
}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(spec))
	}))
	defer server.Close()

	// Create temporary output directory
	outputDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := runImportCommand([]string{
		"--from-url", server.URL,
		"--output", filepath.Join(outputDir, "fragment.yaml"),
		"--timeout", "30s",
	}, &stdout, &stderr)

	// Should succeed
	if code != 0 {
		t.Errorf("Expected return code 0 for successful import, got %d: stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	// Check that output file was created
	outputFile := filepath.Join(outputDir, "fragment.yaml")
	if _, err := os.Stat(outputFile); os.IsNotExist(err) {
		t.Errorf("Expected output file %s to be created", outputFile)
	}

	// Check output contains expected guidance
	output := stdout.String()
	if !strings.Contains(output, "IMPORTANT: Add the following fields manually") {
		t.Errorf("Expected guidance message in output, got: %s", output)
	}
}

func TestDeriveOwnerFromURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected string
	}{
		{
			name:     "simple domain",
			url:      "https://api.example.com",
			expected: "api-example",
		},
		{
			name:     "with subdomain",
			url:      "https://api.service.example.com",
			expected: "api-service-example",
		},
		{
			name:     "with path",
			url:      "https://api.example.com/v1",
			expected: "api-example",
		},
		{
			name:     "with port",
			url:      "https://api.example.com:8443",
			expected: "api-example",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := url.Parse(tt.url)
			if err != nil {
				t.Fatalf("Failed to parse URL: %v", err)
			}
			result := deriveOwnerFromURL(u)
			if result != tt.expected {
				t.Errorf("Expected owner %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestParseCommaSeparatedList(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: nil,
		},
		{
			name:     "single item",
			input:    "get",
			expected: []string{"get"},
		},
		{
			name:     "multiple items",
			input:    "get,post,delete",
			expected: []string{"get", "post", "delete"},
		},
		{
			name:     "with spaces",
			input:    "get, post, delete",
			expected: []string{"get", "post", "delete"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseCommaSeparatedList(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("Expected %d items, got %d", len(tt.expected), len(result))
			}
			for i, item := range result {
				if item != tt.expected[i] {
					t.Errorf("Expected item %d to be %q, got %q", i, tt.expected[i], item)
				}
			}
		})
	}
}

// TestImportFromRealArgoCD tests importing from the actual ArgoCD OpenAPI spec
// This is an integration test that requires access to an ArgoCD instance
func TestImportFromRealArgoCD(t *testing.T) {
	// This test is only run when explicitly enabled (e.g., in CI with ArgoCD access)
	if os.Getenv("SEAM_TEST_ARGOCD_INTEGRATION") == "" {
		t.Skip("Skipping ArgoCD integration test (set SEAM_TEST_ARGOCD_INTEGRATION=1 to enable)")
	}

	// Try to reach ArgoCD through SEAM's proxy (as configured in plan.md)
	// SEAM is at https://seam-rs-manager.tail1b1987.ts.net
	argocdURL := "https://seam-rs-manager.tail1b1987.ts.net/argocd/openapi.json"

	// Create temporary output directory
	outputDir := t.TempDir()
	outputFile := filepath.Join(outputDir, "argocd-fragment.yaml")

	var stdout, stderr bytes.Buffer
	code := runImportCommand([]string{
		"--from-url", argocdURL,
		"--owner", "argocd",
		"--output", outputFile,
		"--timeout", "30s",
	}, &stdout, &stderr)

	// Should succeed or fail with a clear error
	if code != 0 {
		// If we can't reach ArgoCD, that's still useful information
		if strings.Contains(stderr.String(), "failed to fetch spec") ||
			strings.Contains(stderr.String(), "HTTP 403") ||
			strings.Contains(stderr.String(), "HTTP 401") {
			t.Logf("ArgoCD integration test: Could not reach ArgoCD (expected in some environments): %s", stderr.String())
			return
		}
		t.Errorf("Expected successful import or reachability error, got %d: stderr=%s", code, stderr.String())
		return
	}

	// If import succeeded, verify the output file exists
	if _, err := os.Stat(outputFile); os.IsNotExist(err) {
		t.Errorf("Expected output file %s to be created", outputFile)
		return
	}

	// Try to lint the imported fragment
	// This assumes seam lint is available in the PATH
	lintCmd := exec.Command("seam", "lint", outputFile)
	lintOutput, err := lintCmd.CombinedOutput()
	if err != nil {
		// Lint found issues - this is expected for an uncurated import
		// The important thing is that the fragment structure is valid enough to lint
		t.Logf("Lint found issues (expected for uncurated import): %s", lintOutput)
	} else {
		t.Logf("Lint passed: %s", lintOutput)
	}

	// Verify the fragment contains expected fields
	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	contentStr := string(content)
	expectedFields := []string{
		"x-seam-schema: v1",
		"x-seam-owner: argocd",
		"openapi: 3.1.0",
	}

	for _, field := range expectedFields {
		if !strings.Contains(contentStr, field) {
			t.Errorf("Expected fragment to contain %q", field)
		}
	}

	// _unversioned is SEAM-assigned; an authored x-api-version fails lint
	// (api-version.invalid), so the bootstrap must not emit one.
	if strings.Contains(contentStr, "x-api-version") {
		t.Errorf("Expected fragment to NOT contain x-api-version (SEAM assigns it at merge time)")
	}
}

// TestImportFromArgoCDFixture tests importing from a fixture file containing
// the ArgoCD OpenAPI spec structure. This runs without needing a live ArgoCD.
func TestImportFromArgoCDFixture(t *testing.T) {
	// Create a test server that serves an ArgoCD-like OpenAPI spec
	// This simulates the structure of https://argocd.example.com/openapi.json
	spec := `{
  "openapi": "3.1.0",
  "info": {
    "title": "ArgoCD",
    "version": "v2.8.0",
    "description": "ArgoCD OpenAPI specification"
  },
  "paths": {
    "/api/v1/applications": {
      "get": {
        "summary": "List applications",
        "description": "Returns a list of all applications",
        "operationId": "listApplications",
        "tags": ["application"],
        "parameters": [
          {
            "name": "projects",
            "in": "query",
            "schema": {
              "type": "array",
              "items": {"type": "string"}
            }
          }
        ],
        "responses": {
          "200": {
            "description": "Success",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "items": {
                      "type": "array",
                      "items": {"$ref": "#/components/schemas/Application"}
                    }
                  }
                }
              }
            }
          }
        }
      }
    },
    "/api/v1/applications/{name}": {
      "get": {
        "summary": "Get application by name",
        "operationId": "getApplication",
        "tags": ["application"],
        "parameters": [
          {
            "name": "name",
            "in": "path",
            "required": true,
            "schema": {"type": "string"}
          }
        ],
        "responses": {
          "200": {
            "description": "Success"
          },
          "404": {
            "description": "Not found"
          }
        }
      },
      "delete": {
        "summary": "Delete application",
        "operationId": "deleteApplication",
        "tags": ["application"],
        "parameters": [
          {
            "name": "name",
            "in": "path",
            "required": true,
            "schema": {"type": "string"}
          }
        ],
        "responses": {
          "200": {
            "description": "Success"
          }
        }
      }
    },
    "/api/v1/projects": {
      "get": {
        "summary": "List projects",
        "operationId": "listProjects",
        "tags": ["project"],
        "responses": {
          "200": {
            "description": "Success"
          }
        }
      }
    }
  },
  "components": {
    "schemas": {
      "Application": {
        "type": "object",
        "properties": {
          "metadata": {"type": "object"},
          "spec": {"type": "object"},
          "status": {"type": "object"}
        }
      }
    }
  }
}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(spec))
	}))
	defer server.Close()

	// Test 1: Import all paths
	t.Run("import all paths", func(t *testing.T) {
		outputDir := t.TempDir()
		outputFile := filepath.Join(outputDir, "argocd-all.yaml")

		var stdout, stderr bytes.Buffer
		code := runImportCommand([]string{
			"--from-url", server.URL,
			"--owner", "argocd",
			"--output", outputFile,
		}, &stdout, &stderr)

		if code != 0 {
			t.Errorf("Expected return code 0, got %d: stderr=%s", code, stderr.String())
		}

		// Verify output file was created
		if _, err := os.Stat(outputFile); os.IsNotExist(err) {
			t.Errorf("Expected output file %s to be created", outputFile)
		}

		// Check that multiple paths were imported
		content, err := os.ReadFile(outputFile)
		if err != nil {
			t.Fatalf("Failed to read output file: %v", err)
		}

		contentStr := string(content)
		// Should have imported 3 paths
		if !strings.Contains(contentStr, "/api/v1/applications") ||
			!strings.Contains(contentStr, "/api/v1/projects") {
			t.Errorf("Expected fragment to contain multiple paths from ArgoCD spec")
		}

		// Check for SEAM metadata
		if !strings.Contains(contentStr, "x-seam-schema: v1") {
			t.Errorf("Expected fragment to contain x-seam-schema")
		}
		if !strings.Contains(contentStr, "x-seam-owner: argocd") {
			t.Errorf("Expected fragment to contain x-seam-owner: argocd")
		}
	})

	// Test 2: Import with path filtering (only /applications)
	t.Run("import with path filter", func(t *testing.T) {
		outputDir := t.TempDir()
		outputFile := filepath.Join(outputDir, "argocd-filtered.yaml")

		var stdout, stderr bytes.Buffer
		code := runImportCommand([]string{
			"--from-url", server.URL,
			"--owner", "argocd",
			"--output", outputFile,
			"--paths", "/api/v1/applications,/api/v1/applications/{name}",
		}, &stdout, &stderr)

		if code != 0 {
			t.Errorf("Expected return code 0 for filtered import, got %d: stderr=%s", code, stderr.String())
		}

		content, err := os.ReadFile(outputFile)
		if err != nil {
			t.Fatalf("Failed to read output file: %v", err)
		}

		contentStr := string(content)
		// Should only have the filtered paths
		if !strings.Contains(contentStr, "/api/v1/applications:") {
			t.Errorf("Expected fragment to contain /api/v1/applications")
		}
		// Should NOT have projects path
		if strings.Contains(contentStr, "/api/v1/projects:") {
			t.Errorf("Expected fragment to NOT contain /api/v1/projects (was filtered out)")
		}
	})

	// Test 3: Import with method filtering (only GET methods)
	t.Run("import with method filter", func(t *testing.T) {
		outputDir := t.TempDir()
		outputFile := filepath.Join(outputDir, "argocd-get-only.yaml")

		var stdout, stderr bytes.Buffer
		code := runImportCommand([]string{
			"--from-url", server.URL,
			"--owner", "argocd",
			"--output", outputFile,
			"--methods", "GET",
		}, &stdout, &stderr)

		if code != 0 {
			t.Errorf("Expected return code 0 for method-filtered import, got %d: stderr=%s", code, stderr.String())
		}

		content, err := os.ReadFile(outputFile)
		if err != nil {
			t.Fatalf("Failed to read output file: %v", err)
		}

		contentStr := string(content)
		// Should have get: but NOT delete:, post:, etc.
		if strings.Contains(contentStr, "delete:") {
			t.Errorf("Expected fragment to NOT contain delete: method (was filtered out)")
		}
		// Should still have get: methods
		if !strings.Contains(contentStr, "get:") {
			t.Errorf("Expected fragment to contain get: methods")
		}
	})

	// Test 4: Import with prefix stripping
	t.Run("import with prefix stripping", func(t *testing.T) {
		outputDir := t.TempDir()
		outputFile := filepath.Join(outputDir, "argocd-stripped.yaml")

		var stdout, stderr bytes.Buffer
		code := runImportCommand([]string{
			"--from-url", server.URL,
			"--owner", "argocd",
			"--output", outputFile,
			"--strip-prefix", "/api/v1",
		}, &stdout, &stderr)

		if code != 0 {
			t.Errorf("Expected return code 0 for prefix-stripped import, got %d: stderr=%s", code, stderr.String())
		}

		content, err := os.ReadFile(outputFile)
		if err != nil {
			t.Fatalf("Failed to read output file: %v", err)
		}

		contentStr := string(content)
		// Should have paths without /api/v1 prefix
		if !strings.Contains(contentStr, "/applications:") {
			t.Errorf("Expected fragment to contain /applications: (prefix stripped)")
		}
		// Should NOT have the full /api/v1 prefix in paths
		if strings.Contains(contentStr, "/api/v1/applications:") {
			t.Errorf("Expected fragment to NOT contain /api/v1/applications: (prefix should be stripped)")
		}
	})
}

// TestLintImportedFragment verifies that an imported fragment can be linted
// This requires seam lint to be available
func TestLintImportedFragment(t *testing.T) {
	// Skip if seam lint is not available
	if _, err := exec.LookPath("seam"); err != nil {
		t.Skip("seam CLI not available in PATH")
	}

	// Create a minimal valid spec
	spec := `{
  "openapi": "3.1.0",
  "info": {
    "title": "Test API",
    "version": "1.0.0"
  },
  "paths": {
    "/test": {
      "get": {
        "summary": "Test endpoint",
        "responses": {
          "200": {
            "description": "Success"
          }
        }
      }
    }
  }
}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(spec))
	}))
	defer server.Close()

	outputDir := t.TempDir()
	outputFile := filepath.Join(outputDir, "test-fragment.yaml")

	var stdout, stderr bytes.Buffer
	code := runImportCommand([]string{
		"--from-url", server.URL,
		"--owner", "test-service",
		"--output", outputFile,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("Import failed: %s", stderr.String())
	}

	// Now lint the imported fragment
	lintCmd := exec.Command("seam", "lint", outputFile)
	lintOutput, err := lintCmd.CombinedOutput()

	// Lint will fail because the fragment doesn't have required fields like x-vault-path
	// But it should run without crashing
	if err != nil {
		t.Logf("Lint failed as expected for incomplete fragment: %s", lintOutput)
	}

	// The important thing is that lint ran and produced output
	if len(lintOutput) == 0 {
		t.Error("Expected lint to produce output (even if errors)")
	}
}

// argocdSwaggerFixture mirrors the shape of ArgoCD's published spec
// (https://<server>/swagger.json): a Swagger 2.0 document with top-level
// definitions referenced as #/definitions/<name>, one path carrying GET and
// DELETE so method filtering is exercised against it.
const argocdSwaggerFixture = `{
  "swagger": "2.0",
  "info": {"title": "ArgoCD API", "version": "v2.8.0"},
  "paths": {
    "/api/v1/applications": {
      "get": {
        "summary": "List applications",
        "operationId": "ApplicationService_List",
        "responses": {
          "200": {"description": "A successful response.", "schema": {"$ref": "#/definitions/v1alpha1ApplicationList"}}
        }
      }
    },
    "/api/v1/applications/{name}": {
      "get": {
        "summary": "Get an application",
        "operationId": "ApplicationService_Get",
        "responses": {
          "200": {"description": "A successful response.", "schema": {"$ref": "#/definitions/v1alpha1Application"}}
        }
      },
      "delete": {
        "summary": "Delete an application",
        "operationId": "ApplicationService_Delete",
        "responses": {
          "200": {"description": "A successful response."}
        }
      }
    }
  },
  "definitions": {
    "v1alpha1Application": {"type": "object", "properties": {"metadata": {"type": "object"}}},
    "v1alpha1ApplicationList": {"type": "object", "properties": {"items": {"type": "array"}}}
  }
}`

// serveFixture serves an OpenAPI/Swagger fixture over HTTP and returns the
// server; callers must defer Close().
func serveFixture(t *testing.T, spec string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(spec))
	}))
	t.Cleanup(server.Close)
	return server
}

// TestImportLintPassesAfterCuration is the Phase 9b acceptance chain: the
// imported bootstrap must reach a lint-clean fragment through curation alone —
// adding credential/scope metadata, never repairing structural defects.
func TestImportLintPassesAfterCuration(t *testing.T) {
	server := serveFixture(t, argocdSwaggerFixture)
	outputDir := t.TempDir()
	// The fragment must live in a directory named after the owner; lint
	// checks x-seam-owner against the parent directory.
	outputFile := filepath.Join(outputDir, "argocd", "fragment.yaml")

	var stdout, stderr bytes.Buffer
	if code := runImportCommand([]string{
		"--from-url", server.URL + "/swagger.json",
		"--owner", "argocd",
		"--output", outputFile,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("Import failed: code=%d stderr=%s", code, stderr.String())
	}

	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}
	generated := string(content)

	// The generated upstream is the spec URL's base (scheme://host), not the
	// spec document URL; the document URL survives only as provenance in
	// info.description.
	for _, line := range strings.Split(generated, "\n") {
		if strings.HasPrefix(line, "x-upstream:") && strings.Contains(line, "/swagger.json") {
			t.Errorf("Expected x-upstream to be the service base URL, got %q", line)
		}
	}
	if !strings.Contains(generated, "imported from "+server.URL+"/swagger.json") {
		t.Errorf("Expected info.description to record the source spec URL for provenance")
	}

	// Swagger 2.0 definitions must be carried across as components.schemas so
	// the imported operations' $ref pointers resolve.
	if !strings.Contains(generated, "components:") || !strings.Contains(generated, "v1alpha1ApplicationList") {
		t.Errorf("Expected Swagger 2.0 definitions to be carried across as components.schemas")
	}

	lint := func(name string) {
		t.Helper()
		report, err := seamspec.LintFiles([]string{outputFile}, seamspec.LintOptions{
			SchemaPath: "../../spec/route-fragment-schema.json",
		})
		if err != nil {
			t.Fatalf("%s: lint engine error: %v", name, err)
		}
		if report.HasErrors() {
			t.Errorf("%s: expected a lint-clean fragment, got %d error(s):", name, len(report.Errors))
			for _, finding := range report.Errors {
				t.Errorf("  [%s] %s", finding.Code, finding.Message)
			}
		}
	}

	// The httptest server is an IP literal, which lint rejects for upstreams
	// by design; a curator points x-upstream at the real named service. This
	// rewrite stands in for that one decision.
	generated = strings.Replace(generated, "x-upstream: http://"+server.Listener.Addr().String(), "x-upstream: https://argocd.example.com", 1)

	// A curated credential-free fragment is a legal pass-through proxy, so
	// the structural bootstrap must already be lint-clean before any
	// credential metadata is added.
	curatedPath := filepath.Join(outputDir, "argocd", "fragment.yaml")
	if err := os.WriteFile(curatedPath, []byte(generated), 0644); err != nil {
		t.Fatalf("Failed to rewrite fragment: %v", err)
	}
	lint("pass-through bootstrap")

	// Curation adds the credential metadata the command's guidance names —
	// and must keep the fragment lint-clean.
	generated = strings.Replace(generated, "x-upstream: https://argocd.example.com\n",
		"x-upstream: https://argocd.example.com\n"+
			"x-vault-path: seam/routes/argocd/argocd-api-token\n"+
			"x-inject-as:\n    kind: bearer\n"+
			"x-required-scope: argocd:read\n", 1)
	if err := os.WriteFile(curatedPath, []byte(generated), 0644); err != nil {
		t.Fatalf("Failed to write curated fragment: %v", err)
	}
	lint("curated fragment")
}

// TestImportHTTPOutputRequiresPlaintextAcknowledgement verifies the http://
// bootstrap carries the schema-mandated plaintext marker so it can lint.
func TestImportHTTPOutputRequiresPlaintextAcknowledgement(t *testing.T) {
	server := serveFixture(t, argocdSwaggerFixture)
	outputDir := t.TempDir()
	outputFile := filepath.Join(outputDir, "imported", "fragment.yaml")

	var stdout, stderr bytes.Buffer
	if code := runImportCommand([]string{
		"--from-url", server.URL,
		"--owner", "imported",
		"--output", outputFile,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("Import failed: code=%d stderr=%s", code, stderr.String())
	}

	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}
	if !strings.Contains(string(content), "x-upstream-plaintext: acknowledged") {
		t.Errorf("Expected http:// upstream to emit x-upstream-plaintext: acknowledged, got:\n%s", content)
	}
	if !strings.Contains(stderr.String(), "http://") {
		t.Errorf("Expected a stderr note about the plaintext acknowledgement, got: %s", stderr.String())
	}
}

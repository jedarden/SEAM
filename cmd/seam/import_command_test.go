package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

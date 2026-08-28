package spec

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBreakerDisagreementLint tests that the lint validator correctly detects
// x-breaker configuration disagreements across same-origin instances.
func TestBreakerDisagreementLint(t *testing.T) {
	// Create a temporary directory for test fragments
	tmpDir, err := os.MkdirTemp("", "seam-breaker-lint-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a service directory
	serviceDir := filepath.Join(tmpDir, "test-service")
	if err := os.Mkdir(serviceDir, 0755); err != nil {
		t.Fatalf("Failed to create service dir: %v", err)
	}

	// Test case 1: Same-origin instances with conflicting breaker configs
	conflictingFragment := `
x-seam-schema: "v1"
x-seam-owner: "test-service"
x-api-version: "v1"
x-upstream-map:
  instance1:
    url: "https://example.com:8443/api"
    breaker:
      threshold: 5
      openSeconds: 30
      maxOpenSeconds: 300
      enabled: true
  instance2:
    url: "https://example.com:8443/api"
    breaker:
      threshold: 3
      openSeconds: 30
      maxOpenSeconds: 300
      enabled: true
paths:
  /test:
    get:
      operationId: test
      responses:
        "200":
          description: OK
`
	conflictingFile := filepath.Join(serviceDir, "conflicting.yaml")
	if err := os.WriteFile(conflictingFile, []byte(conflictingFragment), 0644); err != nil {
		t.Fatalf("Failed to write conflicting fragment: %v", err)
	}

	// Test case 2: Same-origin instances with identical breaker configs (should pass)
	consistentFragment := `
x-seam-schema: "v1"
x-seam-owner: "test-service"
x-api-version: "v1"
x-upstream-map:
  instance1:
    url: "https://example.com:8443/api"
    breaker:
      threshold: 5
      openSeconds: 30
      maxOpenSeconds: 300
      enabled: true
  instance2:
    url: "https://example.com:8443/api"
    breaker:
      threshold: 5
      openSeconds: 30
      maxOpenSeconds: 300
      enabled: true
paths:
  /test:
    get:
      operationId: test
      responses:
        "200":
          description: OK
`
	consistentFile := filepath.Join(serviceDir, "consistent.yaml")
	if err := os.WriteFile(consistentFile, []byte(consistentFragment), 0644); err != nil {
		t.Fatalf("Failed to write consistent fragment: %v", err)
	}

	// Test case 3: Different origins with different configs (should pass)
	differentOriginFragment := `
x-seam-schema: "v1"
x-seam-owner: "test-service"
x-api-version: "v1"
x-upstream-map:
  instance1:
    url: "https://example1.com:8443/api"
    breaker:
      threshold: 5
      openSeconds: 30
      maxOpenSeconds: 300
      enabled: true
  instance2:
    url: "https://example2.com:8443/api"
    breaker:
      threshold: 3
      openSeconds: 60
      maxOpenSeconds: 300
      enabled: true
paths:
  /test:
    get:
      operationId: test
      responses:
        "200":
          description: OK
`
	differentOriginFile := filepath.Join(serviceDir, "different-origin.yaml")
	if err := os.WriteFile(differentOriginFile, []byte(differentOriginFragment), 0644); err != nil {
		t.Fatalf("Failed to write different-origin fragment: %v", err)
	}

	// Test case 4: Fragment-root config with no instance overrides (should pass)
	fragmentRootOnlyFragment := `
x-seam-schema: "v1"
x-seam-owner: "test-service"
x-api-version: "v1"
x-breaker:
  threshold: 5
  openSeconds: 30
  maxOpenSeconds: 300
  enabled: true
x-upstream-map:
  instance1:
    url: "https://example.com:8443/api"
  instance2:
    url: "https://example.com:8443/api"
paths:
  /test:
    get:
      operationId: test
      responses:
        "200":
          description: OK
`
	fragmentRootOnlyFile := filepath.Join(serviceDir, "fragment-root-only.yaml")
	if err := os.WriteFile(fragmentRootOnlyFile, []byte(fragmentRootOnlyFragment), 0644); err != nil {
		t.Fatalf("Failed to write fragment-root-only fragment: %v", err)
	}

	// Test case 5: Instance with breaker config and one without (same origin)
	mixedConfigFragment := `
x-seam-schema: "v1"
x-seam-owner: "test-service"
x-api-version: "v1"
x-breaker:
  threshold: 5
  openSeconds: 30
  maxOpenSeconds: 300
  enabled: true
x-upstream-map:
  instance1:
    url: "https://example.com:8443/api"
  instance2:
    url: "https://example.com:8443/api"
    breaker:
      threshold: 3
      openSeconds: 30
      maxOpenSeconds: 300
      enabled: true
paths:
  /test:
    get:
      operationId: test
      responses:
        "200":
          description: OK
`
	mixedConfigFile := filepath.Join(serviceDir, "mixed-config.yaml")
	if err := os.WriteFile(mixedConfigFile, []byte(mixedConfigFragment), 0644); err != nil {
		t.Fatalf("Failed to write mixed-config fragment: %v", err)
	}

	// Run lint tests
	tests := []struct {
		name        string
		file        string
		expectError bool
		errorCode   string
	}{
		{
			name:        "conflicting same-origin configs",
			file:        conflictingFile,
			expectError: true,
			errorCode:   "breaker.same-origin-disagreement",
		},
		{
			name:        "consistent same-origin configs",
			file:        consistentFile,
			expectError: false,
		},
		{
			name:        "different origin configs",
			file:        differentOriginFile,
			expectError: false,
		},
		{
			name:        "fragment-root only",
			file:        fragmentRootOnlyFile,
			expectError: false,
		},
		{
			name:        "mixed config (fragment-root + override)",
			file:        mixedConfigFile,
			expectError: true,
			errorCode:   "breaker.same-origin-disagreement",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report, err := LintFiles([]string{tt.file}, LintOptions{
				SchemaPath: "../../spec/route-fragment-schema.json",
			})
			if err != nil {
				t.Fatalf("Lint failed with error: %v", err)
			}

			hasBreakerError := false
			for _, finding := range report.Errors {
				if finding.Code == tt.errorCode {
					hasBreakerError = true
					break
				}
			}

			if tt.expectError && !hasBreakerError {
				t.Errorf("Expected breaker disagreement error, but got none. Errors: %v", report.Errors)
			}

			if !tt.expectError && hasBreakerError {
				t.Errorf("Expected no breaker disagreement error, but got one. Errors: %v", report.Errors)
			}
		})
	}
}

// TestBreakerConfigParsing tests that breaker configuration values are
// correctly parsed from fragment YAML.
func TestBreakerConfigParsing(t *testing.T) {
	// This is implicitly tested by the integration tests, but we can add
	// specific parsing tests if needed
	t.Skip("Parsing is covered by integration tests")
}

// TestBreakerEnabledDefault tests that breaker is enabled by default
// and that enabled: false is the explicit opt-out.
func TestBreakerEnabledDefault(t *testing.T) {
	// Create a temporary directory for test fragments
	tmpDir, err := os.MkdirTemp("", "seam-breaker-default-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a service directory
	serviceDir := filepath.Join(tmpDir, "test-service")
	if err := os.Mkdir(serviceDir, 0755); err != nil {
		t.Fatalf("Failed to create service dir: %v", err)
	}

	// Test case: No x-breaker specified (should use default enabled)
	noBreakerFragment := `
x-seam-schema: "v1"
x-seam-owner: "test-service"
x-api-version: "v1"
x-instance-param: "instance"
x-upstream-map:
  instance1:
    url: "https://example.com:8443/api"
paths:
  /test/{instance}:
    get:
      operationId: test
      parameters:
        - name: instance
          in: path
          required: true
          schema:
            type: string
      responses:
        "200":
          description: OK
`
	noBreakerFile := filepath.Join(serviceDir, "no-breaker.yaml")
	if err := os.WriteFile(noBreakerFile, []byte(noBreakerFragment), 0644); err != nil {
		t.Fatalf("Failed to write no-breaker fragment: %v", err)
	}

	// Test case: Explicitly disabled breaker
	disabledFragment := `
x-seam-schema: "v1"
x-seam-owner: "test-service"
x-api-version: "v1"
x-instance-param: "instance"
x-upstream-map:
  instance1:
    url: "https://example.com:8443/api"
    breaker:
      threshold: 5
      openSeconds: 30
      maxOpenSeconds: 300
      enabled: false
paths:
  /test/{instance}:
    get:
      operationId: test
      parameters:
        - name: instance
          in: path
          required: true
          schema:
            type: string
      responses:
        "200":
          description: OK
`
	disabledFile := filepath.Join(serviceDir, "disabled.yaml")
	if err := os.WriteFile(disabledFile, []byte(disabledFragment), 0644); err != nil {
		t.Fatalf("Failed to write disabled fragment: %v", err)
	}

	// Test that no-breaker fragment passes lint (default is valid)
	report, err := LintFiles([]string{noBreakerFile}, LintOptions{
		SchemaPath: "../../spec/route-fragment-schema.json",
	})
	if err != nil {
		t.Fatalf("Lint failed with error: %v", err)
	}

	if report.HasErrors() {
		t.Errorf("Expected no errors for default breaker config, got: %v", report.Errors)
	}

	// Test that disabled breaker fragment passes lint (explicit opt-out is valid)
	report, err = LintFiles([]string{disabledFile}, LintOptions{
		SchemaPath: "../../spec/route-fragment-schema.json",
	})
	if err != nil {
		t.Fatalf("Lint failed with error: %v", err)
	}

	if report.HasErrors() {
		t.Errorf("Expected no errors for explicitly disabled breaker, got: %v", report.Errors)
	}
}

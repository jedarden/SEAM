package spec

import (
	"os"
	"path/filepath"
	"testing"
)

// TestVaultPathTraversal tests that vault paths with traversal attempts are rejected
func TestVaultPathTraversal(t *testing.T) {
	enforcer, _ := NewAllowlistEnforcer("seam/routes", "")

	tests := []struct {
		name      string
		vaultPath string
		owner     string
		wantErr   bool
		errMsg    string
	}{
		{
			name:      "double_dot_traversal",
			vaultPath: "seam/routes/myowner/../../../etc/passwd",
			owner:     "myowner",
			wantErr:   true,
			errMsg:    "vault_path_contains_traversal",
		},
		{
			name:      "backslash_traversal",
			vaultPath: "seam\\routes\\myowner\\secret",
			owner:     "myowner",
			wantErr:   true,
			errMsg:    "vault_path_contains_traversal",
		},
		{
			name:      "double_dot_simple",
			vaultPath: "../secret",
			owner:     "myowner",
			wantErr:   true,
			errMsg:    "vault_path_contains_traversal",
		},
		{
			name:      "valid_path",
			vaultPath: "seam/routes/myowner/api/key",
			owner:     "myowner",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := enforcer.ValidateVaultPath(tt.vaultPath, tt.owner)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateVaultPath() expected error containing %q, got nil", tt.errMsg)
				} else if err != nil && !containsString(err.Error(), tt.errMsg) {
					t.Errorf("ValidateVaultPath() expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("ValidateVaultPath() unexpected error: %v", err)
				}
			}
		})
	}
}

// TestVaultPathGlobs tests that vault paths with glob patterns are rejected
func TestVaultPathGlobs(t *testing.T) {
	enforcer, _ := NewAllowlistEnforcer("seam/routes", "")

	tests := []struct {
		name      string
		vaultPath string
		owner     string
		wantErr   bool
		errMsg    string
	}{
		{
			name:      "asterisk_glob",
			vaultPath: "seam/routes/myowner/*/secret",
			owner:     "myowner",
			wantErr:   true,
			errMsg:    "vault_path_contains_globs",
		},
		{
			name:      "question_mark_glob",
			vaultPath: "seam/routes/myowner/secre?",
			owner:     "myowner",
			wantErr:   true,
			errMsg:    "vault_path_contains_globs",
		},
		{
			name:      "bracket_glob",
			vaultPath: "seam/routes/myowner/secret[abc]",
			owner:     "myowner",
			wantErr:   true,
			errMsg:    "vault_path_contains_globs",
		},
		{
			name:      "valid_path_no_globs",
			vaultPath: "seam/routes/myowner/api/production/key",
			owner:     "myowner",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := enforcer.ValidateVaultPath(tt.vaultPath, tt.owner)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateVaultPath() expected error containing %q, got nil", tt.errMsg)
				} else if err != nil && !containsString(err.Error(), tt.errMsg) {
					t.Errorf("ValidateVaultPath() expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("ValidateVaultPath() unexpected error: %v", err)
				}
			}
		})
	}
}

// TestVaultPathTemplates tests that vault paths with OpenAPI-style templates are rejected
func TestVaultPathTemplates(t *testing.T) {
	enforcer, _ := NewAllowlistEnforcer("seam/routes", "")

	tests := []struct {
		name      string
		vaultPath string
		owner     string
		wantErr   bool
		errMsg    string
	}{
		{
			name:      "openapi_template",
			vaultPath: "seam/routes/myowner/{env}/secret",
			owner:     "myowner",
			wantErr:   true,
			errMsg:    "vault_path_contains_templates",
		},
		{
			name:      "template_close_only",
			vaultPath: "seam/routes/myowner/secret}",
			owner:     "myowner",
			wantErr:   true,
			errMsg:    "vault_path_contains_templates",
		},
		{
			name:      "template_open_only",
			vaultPath: "seam/routes/myowner/{secret",
			owner:     "myowner",
			wantErr:   true,
			errMsg:    "vault_path_contains_templates",
		},
		{
			name:      "valid_path_no_templates",
			vaultPath: "seam/routes/myowner/production/api/key",
			owner:     "myowner",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := enforcer.ValidateVaultPath(tt.vaultPath, tt.owner)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateVaultPath() expected error containing %q, got nil", tt.errMsg)
				} else if err != nil && !containsString(err.Error(), tt.errMsg) {
					t.Errorf("ValidateVaultPath() expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("ValidateVaultPath() unexpected error: %v", err)
				}
			}
		})
	}
}

// TestVaultPathOwnership tests that vault paths must be inside the owner's directory
func TestVaultPathOwnership(t *testing.T) {
	enforcer, _ := NewAllowlistEnforcer("seam/routes", "")

	tests := []struct {
		name      string
		vaultPath string
		owner     string
		wantErr   bool
		errMsg    string
	}{
		{
			name:      "valid_path_inside_owner_dir",
			vaultPath: "seam/routes/myowner/production/secret",
			owner:     "myowner",
			wantErr:   false,
		},
		{
			name:      "path_outside_owner_dir_different_owner",
			vaultPath: "seam/routes/otherowner/secret",
			owner:     "myowner",
			wantErr:   true,
			errMsg:    "vault_path_outside_owner_directory",
		},
		{
			name:      "path_at_root_not_in_owner_dir",
			vaultPath: "seam/routes/shared_secret",
			owner:     "myowner",
			wantErr:   true,
			errMsg:    "vault_path_outside_owner_directory",
		},
		{
			name:      "empty_path_allowed",
			vaultPath: "",
			owner:     "myowner",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := enforcer.ValidateVaultPath(tt.vaultPath, tt.owner)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateVaultPath() expected error containing %q, got nil", tt.errMsg)
				} else if err != nil && !containsString(err.Error(), tt.errMsg) {
					t.Errorf("ValidateVaultPath() expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("ValidateVaultPath() unexpected error: %v", err)
				}
			}
		})
	}
}

// TestUpstreamHostFailClosed tests that the allowlist fails closed when absent/empty/unparseable
func TestUpstreamHostFailClosed(t *testing.T) {
	tests := []struct {
		name          string
		allowlistFile string
		upstreamURL   string
		wantErr       bool
		errMsg        string
	}{
		{
			name:          "no_allowlist_configured",
			allowlistFile: "",
			upstreamURL:   "http://example.com/api",
			wantErr:       true,
			errMsg:        "allowlist_not_loaded",
		},
		{
			name:          "empty_upstream_url_allowed",
			allowlistFile: "",
			upstreamURL:   "",
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enforcer, _ := NewAllowlistEnforcer("seam/routes", tt.allowlistFile)

			err := enforcer.ValidateUpstreamHost(tt.upstreamURL)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateUpstreamHost() expected error containing %q, got nil", tt.errMsg)
				} else if err != nil && !containsString(err.Error(), tt.errMsg) {
					t.Errorf("ValidateUpstreamHost() expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("ValidateUpstreamHost() unexpected error: %v", err)
				}
			}
		})
	}
}

// TestUpstreamHostIPLiteralRejection tests that IP literals are rejected
func TestUpstreamHostIPLiteralRejection(t *testing.T) {
	// Create a temporary allowlist file
	tmpDir := t.TempDir()
	allowlistFile := filepath.Join(tmpDir, "allowlist.yaml")
	content := "- .example.com\n- localhost\n"
	if err := os.WriteFile(allowlistFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create allowlist file: %v", err)
	}

	enforcer, err := NewAllowlistEnforcer("seam/routes", allowlistFile)
	if err != nil {
		t.Fatalf("Failed to create enforcer: %v", err)
	}

	tests := []struct {
		name        string
		upstreamURL string
		wantErr     bool
		errMsg      string
	}{
		{
			name:        "ipv4_literal",
			upstreamURL: "http://192.168.1.1/api",
			wantErr:     true,
			errMsg:      "upstream_host_is_ip_literal",
		},
		{
			name:        "ipv6_literal",
			upstreamURL: "http://[2001:db8::1]/api",
			wantErr:     true,
			errMsg:      "upstream_host_is_ip_literal",
		},
		{
			name:        "ipv4_with_port",
			upstreamURL: "http://10.0.0.1:8080/api",
			wantErr:     true,
			errMsg:      "upstream_host_is_ip_literal",
		},
		{
			name:        "valid_hostname_allowed",
			upstreamURL: "http://api.example.com/endpoint",
			wantErr:     false,
		},
		{
			name:        "localhost_allowed",
			upstreamURL: "http://localhost:8080/api",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := enforcer.ValidateUpstreamHost(tt.upstreamURL)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateUpstreamHost() expected error containing %q, got nil", tt.errMsg)
				} else if err != nil && !containsString(err.Error(), tt.errMsg) {
					t.Errorf("ValidateUpstreamHost() expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("ValidateUpstreamHost() unexpected error: %v", err)
				}
			}
		})
	}
}

// TestUpstreamHostSuffixMatching tests suffix matching rules
func TestUpstreamHostSuffixMatching(t *testing.T) {
	// Create a temporary allowlist file with suffixes
	tmpDir := t.TempDir()
	allowlistFile := filepath.Join(tmpDir, "allowlist.yaml")
	content := `- .example.com
- .internal
- api.production
`
	if err := os.WriteFile(allowlistFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create allowlist file: %v", err)
	}

	enforcer, err := NewAllowlistEnforcer("seam/routes", allowlistFile)
	if err != nil {
		t.Fatalf("Failed to create enforcer: %v", err)
	}

	tests := []struct {
		name        string
		upstreamURL string
		wantErr     bool
		errMsg      string
	}{
		{
			name:        "subdomain_matches_suffix",
			upstreamURL: "http://api.example.com/endpoint",
			wantErr:     false,
		},
		{
			name:        "deep_subdomain_matches_suffix",
			upstreamURL: "http://v1.api.example.com/endpoint",
			wantErr:     false,
		},
		{
			name:        "internal_subdomain_matches",
			upstreamURL: "http://service.internal/endpoint",
			wantErr:     false,
		},
		{
			name:        "bare_hostname_match",
			upstreamURL: "http://api.production/endpoint",
			wantErr:     false,
		},
		{
			name:        "suffix_match_not_partial",
			upstreamURL: "http://fakeexample.com/endpoint",
			wantErr:     true,
			errMsg:      "upstream_host_not_in_allowlist",
		},
		{
			name:        "different_domain_rejected",
			upstreamURL: "http://api.evil.com/endpoint",
			wantErr:     true,
			errMsg:      "upstream_host_not_in_allowlist",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := enforcer.ValidateUpstreamHost(tt.upstreamURL)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateUpstreamHost() expected error containing %q, got nil", tt.errMsg)
				} else if err != nil && !containsString(err.Error(), tt.errMsg) {
					t.Errorf("ValidateUpstreamHost() expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("ValidateUpstreamHost() unexpected error: %v", err)
				}
			}
		})
	}
}

// TestIsFailClosed tests the fail-closed detection
func TestIsFailClosed(t *testing.T) {
	t.Run("no_allowlist_fail_closed", func(t *testing.T) {
		enforcer, _ := NewAllowlistEnforcer("seam/routes", "")
		if !enforcer.IsFailClosed() {
			t.Error("IsFailClosed() should return true when no allowlist is loaded")
		}
	})

	t.Run("loaded_allowlist_not_fail_closed", func(t *testing.T) {
		tmpDir := t.TempDir()
		allowlistFile := filepath.Join(tmpDir, "allowlist.yaml")
		content := "- .example.com\n"
		if err := os.WriteFile(allowlistFile, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create allowlist file: %v", err)
		}

		enforcer, err := NewAllowlistEnforcer("seam/routes", allowlistFile)
		if err != nil {
			t.Fatalf("Failed to create enforcer: %v", err)
		}

		if enforcer.IsFailClosed() {
			t.Error("IsFailClosed() should return false when allowlist is loaded")
		}
	})

	t.Run("empty_allowlist_fail_closed", func(t *testing.T) {
		tmpDir := t.TempDir()
		allowlistFile := filepath.Join(tmpDir, "allowlist.yaml")
		content := "# Empty allowlist\n"
		if err := os.WriteFile(allowlistFile, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create allowlist file: %v", err)
		}

		enforcer, err := NewAllowlistEnforcer("seam/routes", allowlistFile)
		if err != nil {
			t.Fatalf("Failed to create enforcer: %v", err)
		}

		if !enforcer.IsFailClosed() {
			t.Error("IsFailClosed() should return true when allowlist is empty")
		}
	})
}

// TestGetAllowlistStatus tests the status reporting
func TestGetAllowlistStatus(t *testing.T) {
	t.Run("no_allowlist_status", func(t *testing.T) {
		enforcer, _ := NewAllowlistEnforcer("seam/routes", "")
		status := enforcer.GetAllowlistStatus()

		if status["source"] != "none" {
			t.Errorf("Expected source 'none', got %v", status["source"])
		}

		if upstream, ok := status["upstream_allowlist"].(map[string]interface{}); ok {
			if loaded, ok := upstream["loaded"].(bool); ok && loaded {
				t.Error("Expected loaded=false when no allowlist configured")
			}
		}
	})

	t.Run("loaded_allowlist_status", func(t *testing.T) {
		tmpDir := t.TempDir()
		allowlistFile := filepath.Join(tmpDir, "allowlist.yaml")
		content := "- .example.com\n- .internal\n"
		if err := os.WriteFile(allowlistFile, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create allowlist file: %v", err)
		}

		enforcer, err := NewAllowlistEnforcer("seam/routes", allowlistFile)
		if err != nil {
			t.Fatalf("Failed to create enforcer: %v", err)
		}

		status := enforcer.GetAllowlistStatus()

		if status["source"] != "dev-file" {
			t.Errorf("Expected source 'dev-file', got %v", status["source"])
		}

		if upstream, ok := status["upstream_allowlist"].(map[string]interface{}); ok {
			if loaded, ok := upstream["loaded"].(bool); !ok || !loaded {
				t.Error("Expected loaded=true when allowlist is loaded successfully")
			}

			if condition, ok := upstream["condition"].(string); !ok || condition != "allowlist_ok" {
				t.Errorf("Expected condition 'allowlist_ok', got %v", condition)
			}
		}
	})
}

// Helper function to check if a string contains a substring
func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

package server

import (
	"testing"
)

// TestConfigStatusBreakerDisagreements tests that the /config/status endpoint
// correctly enumerates breaker configuration disagreements for operator visibility.
func TestConfigStatusBreakerDisagreements(t *testing.T) {
	// This is a structural test to verify the enumeration method exists and works
	// Full integration testing would require a full server setup
	t.Run("enumeration method exists", func(t *testing.T) {
		// New initializes the spec loader from cfg.SpecDir and log.Fatals when it
		// cannot, so point it at the repo spec and a baseline allowlist the way
		// TestConfigStatusReportsRuntimeStateWithoutSecrets does.
		cfg := &Config{
			CallerPort:    8080,
			OperatorPort:  8081,
			BaseURL:       "https://operator:REPLACE@example.test:8443?token=REPLACE",
			SpecDir:       "../../spec",
			AllowlistFile: newBaselineAllowlistFile(t),
		}

		s := New(cfg)

		// Call the enumeration method
		result := s.enumerateBreakerDisagreements()

		// Verify result structure
		if result == nil {
			t.Fatal("Expected non-nil result from enumerateBreakerDisagreements")
		}

		// Check for expected keys
		hasDisagreements, ok := result["has_disagreements"].(bool)
		if !ok {
			t.Error("Expected has_disagreements to be a bool")
		}

		if hasDisagreements {
			disagreements, ok := result["disagreements"]
			if !ok {
				t.Error("Expected disagreements key when has_disagreements is true")
			}

			if disagreements == nil {
				t.Error("Expected disagreements array to be non-nil when has_disagreements is true")
			}
		} else {
			// When no disagreements, should have empty array
			disagreements, ok := result["disagreements"]
			if !ok {
				t.Error("Expected disagreements key even when has_disagreements is false")
			}

			if disagreements == nil {
				t.Error("Expected disagreements array to be non-nil (empty array) when has_disagreements is false")
			}
		}
	})
}

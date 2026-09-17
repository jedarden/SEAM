package spec

import (
	"testing"
)

// TestCheckDeprecation_BareTrue tests that bare true is a lint error
func TestCheckDeprecation_BareTrue(t *testing.T) {
	fragment := &lintFragment{
		file: "test.yaml",
		data: map[string]any{
			"x-seam-deprecated": true,
		},
	}

	report := &LintReport{}
	fragment.checkDeprecation(report)

	if len(report.Errors) != 1 {
		t.Errorf("Expected 1 error, got %d", len(report.Errors))
	}

	if report.Errors[0].Code != "deprecation.bare-true" {
		t.Errorf("Expected error code 'deprecation.bare-true', got %q", report.Errors[0].Code)
	}
}

// TestCheckDeprecation_MissingSince tests that missing since is a lint error
func TestCheckDeprecation_MissingSince(t *testing.T) {
	fragment := &lintFragment{
		file: "test.yaml",
		data: map[string]any{
			"x-seam-deprecated": map[string]any{},
		},
	}

	report := &LintReport{}
	fragment.checkDeprecation(report)

	if len(report.Errors) != 1 {
		t.Errorf("Expected 1 error, got %d", len(report.Errors))
	}

	if report.Errors[0].Code != "deprecation.since-missing" {
		t.Errorf("Expected error code 'deprecation.since-missing', got %q", report.Errors[0].Code)
	}
}

// TestCheckDeprecation_InvalidSince tests that invalid since date is a lint error
func TestCheckDeprecation_InvalidSince(t *testing.T) {
	fragment := &lintFragment{
		file: "test.yaml",
		data: map[string]any{
			"x-seam-deprecated": map[string]any{
				"since": "2024-13-01", // Invalid month
			},
		},
	}

	report := &LintReport{}
	fragment.checkDeprecation(report)

	// Should have both format and parse errors
	if len(report.Errors) == 0 {
		t.Errorf("Expected at least 1 error for invalid since date, got 0")
	}
}

// TestCheckDeprecation_ValidSince tests that valid since passes
func TestCheckDeprecation_ValidSince(t *testing.T) {
	fragment := &lintFragment{
		file: "test.yaml",
		data: map[string]any{
			"x-seam-deprecated": map[string]any{
				"since": "2024-01-15",
			},
		},
	}

	report := &LintReport{}
	fragment.checkDeprecation(report)

	if len(report.Errors) != 0 {
		t.Errorf("Expected no errors for valid deprecation, got %d", len(report.Errors))
		for _, e := range report.Errors {
			t.Logf("Error: %s - %s", e.Code, e.Message)
		}
	}
}

// TestCheckDeprecation_SunsetBeforeSince tests that sunset before since is an error
func TestCheckDeprecation_SunsetBeforeSince(t *testing.T) {
	fragment := &lintFragment{
		file: "test.yaml",
		data: map[string]any{
			"x-seam-deprecated": map[string]any{
				"since":  "2024-06-01",
				"sunset": "2024-01-01", // Before since
			},
		},
	}

	report := &LintReport{}
	fragment.checkDeprecation(report)

	if len(report.Errors) != 1 {
		t.Errorf("Expected 1 error, got %d", len(report.Errors))
	}

	if report.Errors[0].Code != "deprecation.sunset-before-since" {
		t.Errorf("Expected error code 'deprecation.sunset-before-since', got %q", report.Errors[0].Code)
	}
}

// TestCheckDeprecation_BrownoutWithoutSunset tests that brownout without sunset is an error
func TestCheckDeprecation_BrownoutWithoutSunset(t *testing.T) {
	fragment := &lintFragment{
		file: "test.yaml",
		data: map[string]any{
			"x-seam-deprecated": map[string]any{
				"since": "2024-01-01",
				"brownout": []map[string]any{
					{
						"start": "2024-06-01T00:00:00Z",
						"end":   "2024-06-01T01:00:00Z",
					},
				},
			},
		},
	}

	report := &LintReport{}
	fragment.checkDeprecation(report)

	if len(report.Errors) != 1 {
		t.Errorf("Expected 1 error, got %d", len(report.Errors))
	}

	if report.Errors[0].Code != "deprecation.brownout-without-sunset" {
		t.Errorf("Expected error code 'deprecation.brownout-without-sunset', got %q", report.Errors[0].Code)
	}
}

// TestCheckDeprecation_OverlappingBrownouts tests that overlapping brownout windows are an error
func TestCheckDeprecation_OverlappingBrownouts(t *testing.T) {
	fragment := &lintFragment{
		file: "test.yaml",
		data: map[string]any{
			"x-seam-deprecated": map[string]any{
				"since":  "2024-01-01",
				"sunset": "2024-12-31",
				"brownout": []map[string]any{
					{
						"start": "2024-06-01T00:00:00Z",
						"end":   "2024-06-01T02:00:00Z",
					},
					{
						"start": "2024-06-01T01:00:00Z", // Overlaps with first
						"end":   "2024-06-01T03:00:00Z",
					},
				},
			},
		},
	}

	report := &LintReport{}
	fragment.checkDeprecation(report)

	if len(report.Errors) != 1 {
		t.Errorf("Expected 1 error, got %d", len(report.Errors))
	}

	if report.Errors[0].Code != "deprecation.brownout-overlapping" {
		t.Errorf("Expected error code 'deprecation.brownout-overlapping', got %q", report.Errors[0].Code)
	}
}

// TestCheckDeprecation_BrownoutEndBeforeStart tests that brownout end before start is an error
func TestCheckDeprecation_BrownoutEndBeforeStart(t *testing.T) {
	fragment := &lintFragment{
		file: "test.yaml",
		data: map[string]any{
			"x-seam-deprecated": map[string]any{
				"since":  "2024-01-01",
				"sunset": "2024-12-31",
				"brownout": []map[string]any{
					{
						"start": "2024-06-01T02:00:00Z",
						"end":   "2024-06-01T01:00:00Z", // Before start
					},
				},
			},
		},
	}

	report := &LintReport{}
	fragment.checkDeprecation(report)

	if len(report.Errors) != 1 {
		t.Errorf("Expected 1 error, got %d", len(report.Errors))
	}

	if report.Errors[0].Code != "deprecation.brownout-end-before-start" {
		t.Errorf("Expected error code 'deprecation.brownout-end-before-start', got %q", report.Errors[0].Code)
	}
}

// TestCheckDeprecation_BrownoutOutOfRange tests that brownout outside [since, sunset] is an error
func TestCheckDeprecation_BrownoutOutOfRange(t *testing.T) {
	fragment := &lintFragment{
		file: "test.yaml",
		data: map[string]any{
			"x-seam-deprecated": map[string]any{
				"since":  "2024-06-01",
				"sunset": "2024-12-31",
				"brownout": []map[string]any{
					{
						"start": "2024-01-01T00:00:00Z", // Before since
						"end":   "2024-01-01T01:00:00Z",
					},
				},
			},
		},
	}

	report := &LintReport{}
	fragment.checkDeprecation(report)

	if len(report.Errors) != 1 {
		t.Errorf("Expected 1 error, got %d", len(report.Errors))
	}

	if report.Errors[0].Code != "deprecation.brownout-out-of-range" {
		t.Errorf("Expected error code 'deprecation.brownout-out-of-range', got %q", report.Errors[0].Code)
	}
}

// TestCheckDeprecation_ValidBrownout tests that valid brownout passes
func TestCheckDeprecation_ValidBrownout(t *testing.T) {
	fragment := &lintFragment{
		file: "test.yaml",
		data: map[string]any{
			"x-seam-deprecated": map[string]any{
				"since":  "2024-01-01",
				"sunset": "2024-12-31",
				"brownout": []map[string]any{
					{
						"start": "2024-06-01T00:00:00Z",
						"end":   "2024-06-01T01:00:00Z",
					},
					{
						"start": "2024-07-01T00:00:00Z", // Sequential, no overlap
						"end":   "2024-07-01T01:00:00Z",
					},
				},
			},
		},
	}

	report := &LintReport{}
	fragment.checkDeprecation(report)

	if len(report.Errors) != 0 {
		t.Errorf("Expected no errors for valid brownout windows, got %d", len(report.Errors))
		for _, e := range report.Errors {
			t.Logf("Error: %s - %s", e.Code, e.Message)
		}
	}
}

// TestIsValidISODate tests the ISO date validation helper
func TestIsValidISODate(t *testing.T) {
	tests := []struct {
		name     string
		date     string
		expected bool
	}{
		{"Valid date", "2024-01-15", true},
		{"Valid date", "2024-12-31", true},
		{"Invalid month", "2024-13-01", false},
		{"Invalid day", "2024-01-32", false},
		{"Missing separators", "20240101", false},
		{"Wrong separator", "2024/01/01", false},
		{"Too short", "2024-01-1", false},
		{"Too long", "2024-01-015", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidISODate(tt.date)
			if result != tt.expected {
				t.Errorf("isValidISODate(%q) = %v, want %v", tt.date, result, tt.expected)
			}
		})
	}
}

// TestIsValidISODateTime tests the ISO date-time validation helper
func TestIsValidISODateTime(t *testing.T) {
	tests := []struct {
		name     string
		datetime string
		expected bool
	}{
		{"Valid RFC 3339 with Z", "2024-01-15T10:30:00Z", true},
		{"Valid RFC 3339 with offset", "2024-01-15T10:30:00+08:00", true},
		{"Valid with space separator", "2024-01-15 10:30:00Z", true},
		{"Too short", "2024-01-15T10:30", false},
		{"Missing date", "10:30:00", false},
		{"Invalid date", "2024-13-01T10:30:00Z", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidISODateTime(tt.datetime)
			if result != tt.expected {
				t.Errorf("isValidISODateTime(%q) = %v, want %v", tt.datetime, result, tt.expected)
			}
		})
	}
}

// TestIsDateTimeWithinRange tests the range checking helper
func TestIsDateTimeWithinRange(t *testing.T) {
	tests := []struct {
		name     string
		datetime string
		since    string
		sunset   string
		expected bool
	}{
		{"Within range", "2024-06-15T10:00:00Z", "2024-01-01", "2024-12-31", true},
		{"Before since", "2023-12-31T23:59:59Z", "2024-01-01", "2024-12-31", false},
		{"After sunset", "2025-01-01T00:00:00Z", "2024-01-01", "2024-12-31", false},
		{"Equal to since", "2024-01-01T00:00:00Z", "2024-01-01", "2024-12-31", true},
		{"Equal to sunset", "2024-12-31T23:59:59Z", "2024-01-01", "2024-12-31", true},
		{"Invalid datetime", "invalid", "2024-01-01", "2024-12-31", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isDateTimeWithinRange(tt.datetime, tt.since, tt.sunset)
			if result != tt.expected {
				t.Errorf("isDateTimeWithinRange(%q, %q, %q) = %v, want %v",
					tt.datetime, tt.since, tt.sunset, result, tt.expected)
			}
		})
	}
}

// TestCheckDeprecation_MixedOffsetAdjacentWindowsAccepted pins that
// adjacency is judged on absolute instants, not written clock strings: the
// first window ends at 21:30Z (23:30+02:00) and the second starts at 22:00Z,
// so the windows do not overlap even though lexicographic string comparison
// of the raw values would order the start before the previous end and flag
// the pair.
func TestCheckDeprecation_MixedOffsetAdjacentWindowsAccepted(t *testing.T) {
	fragment := &lintFragment{
		file: "test.yaml",
		data: map[string]any{
			"x-seam-deprecated": map[string]any{
				"since":  "2024-01-01",
				"sunset": "2024-12-31",
				"brownout": []map[string]any{
					{
						"start": "2024-06-15T00:00:00Z",
						"end":   "2024-06-15T23:30:00+02:00", // == 21:30Z
					},
					{
						"start": "2024-06-15T22:00:00Z", // after 21:30Z: legal adjacency
						"end":   "2024-06-15T23:00:00Z",
					},
				},
			},
		},
	}

	report := &LintReport{}
	fragment.checkDeprecation(report)

	if len(report.Errors) != 0 {
		t.Errorf("Expected no errors for instants-ordered mixed-offset windows, got %d", len(report.Errors))
		for _, e := range report.Errors {
			t.Logf("Error: %s - %s", e.Code, e.Message)
		}
	}
}

// TestCheckDeprecation_MixedOffsetOverlapDetected pins the false-negative
// direction: the first window ends at 04:00Z on the 16th (23:00-05:00 on the
// 15th) and the second starts 01:00Z on the 16th — a real overlap that
// written-date string comparison would miss because the second window's date
// part sorts after the first's.
func TestCheckDeprecation_MixedOffsetOverlapDetected(t *testing.T) {
	fragment := &lintFragment{
		file: "test.yaml",
		data: map[string]any{
			"x-seam-deprecated": map[string]any{
				"since":  "2024-01-01",
				"sunset": "2024-12-31",
				"brownout": []map[string]any{
					{
						"start": "2024-06-15T00:00:00Z",
						"end":   "2024-06-15T23:00:00-05:00", // == 2024-06-16T04:00Z
					},
					{
						"start": "2024-06-16T01:00:00Z", // before 04:00Z: overlap
						"end":   "2024-06-16T02:00:00Z",
					},
				},
			},
		},
	}

	report := &LintReport{}
	fragment.checkDeprecation(report)

	if len(report.Errors) != 1 {
		t.Errorf("Expected 1 error for the instants-overlapping pair, got %d", len(report.Errors))
		for _, e := range report.Errors {
			t.Logf("Error: %s - %s", e.Code, e.Message)
		}
		return
	}
	if report.Errors[0].Code != "deprecation.brownout-overlapping" {
		t.Errorf("Expected error code 'deprecation.brownout-overlapping', got %q", report.Errors[0].Code)
	}
}

// TestCheckDeprecation_WindowPastSunsetOffsetDetected pins the [since,
// sunset] range check on absolute instants: an end written on the sunset
// calendar day in a westward offset lands after the sunset day in absolute
// terms and must be flagged.
func TestCheckDeprecation_WindowPastSunsetOffsetDetected(t *testing.T) {
	fragment := &lintFragment{
		file: "test.yaml",
		data: map[string]any{
			"x-seam-deprecated": map[string]any{
				"since":  "2024-01-01",
				"sunset": "2024-12-31",
				"brownout": []map[string]any{
					{
						"start": "2024-12-31T10:00:00Z",
						"end":   "2024-12-31T23:00:00-05:00", // == 2025-01-01T04:00Z: past sunset
					},
				},
			},
		},
	}

	report := &LintReport{}
	fragment.checkDeprecation(report)

	if len(report.Errors) != 1 {
		t.Errorf("Expected 1 error for an end instant past sunset, got %d", len(report.Errors))
		for _, e := range report.Errors {
			t.Logf("Error: %s - %s", e.Code, e.Message)
		}
		return
	}
	if report.Errors[0].Code != "deprecation.brownout-out-of-range" {
		t.Errorf("Expected error code 'deprecation.brownout-out-of-range', got %q", report.Errors[0].Code)
	}
}

// TestParseBrownoutInstant pins the shared parse: absolute instants, with the
// lenient separators isValidISODateTime accepts normalised so every
// lint-valid window is parseable.
func TestParseBrownoutInstant(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantOK  bool
		wantUTC string // expected rendering in UTC; ignored when wantOK is false
	}{
		{"UTC instant", "2024-06-15T10:00:00Z", true, "2024-06-15 10:00:00 +0000 UTC"},
		{"lower-case z", "2024-06-15T10:00:00z", true, "2024-06-15 10:00:00 +0000 UTC"},
		{"space separator", "2024-06-15 10:00:00Z", true, "2024-06-15 10:00:00 +0000 UTC"},
		{"lower-case t", "2024-06-15t10:00:00Z", true, "2024-06-15 10:00:00 +0000 UTC"},
		{"non-UTC offset", "2024-06-15T12:00:00+02:00", true, "2024-06-15 10:00:00 +0000 UTC"},
		{"negative offset", "2024-06-15T04:00:00-04:00", true, "2024-06-15 08:00:00 +0000 UTC"},
		{"date only", "2024-06-15", false, ""},
		{"missing zone", "2024-06-15T10:00:00", false, ""},
		{"garbage", "not-a-timestamp", false, ""},
		{"empty", "", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseBrownoutInstant(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("parseBrownoutInstant(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if gotUTC := got.UTC().String(); gotUTC != tt.wantUTC {
				t.Errorf("parseBrownoutInstant(%q) = %s, want %s", tt.input, gotUTC, tt.wantUTC)
			}
		})
	}
}

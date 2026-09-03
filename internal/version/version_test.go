package version

import (
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantName  string
		wantRank  int
		wantValid bool
	}{
		{
			name:      "default unversioned",
			input:     "_unversioned",
			wantName:  "_unversioned",
			wantRank:  0,
			wantValid: true,
		},
		{
			name:      "empty string becomes default",
			input:     "",
			wantName:  "_unversioned",
			wantRank:  0,
			wantValid: true,
		},
		{
			name:      "v1",
			input:     "v1",
			wantName:  "v1",
			wantRank:  1,
			wantValid: true,
		},
		{
			name:      "v2",
			input:     "v2",
			wantName:  "v2",
			wantRank:  2,
			wantValid: true,
		},
		{
			name:      "v10",
			input:     "v10",
			wantName:  "v10",
			wantRank:  10,
			wantValid: true,
		},
		{
			name:      "v99",
			input:     "v99",
			wantName:  "v99",
			wantRank:  99,
			wantValid: true,
		},
		{
			name:      "v0 is invalid (grammar is v[1-9][0-9]*)",
			input:     "v0",
			wantName:  "v0",
			wantRank:  -1,
			wantValid: false,
		},
		{
			name:      "invalid - no v prefix",
			input:     "1",
			wantName:  "1",
			wantRank:  -1,
			wantValid: false,
		},
		{
			name:      "invalid - leading zero",
			input:     "v01",
			wantName:  "v01",
			wantRank:  -1,
			wantValid: false,
		},
		{
			name:      "invalid - non-numeric after v",
			input:     "vx",
			wantName:  "vx",
			wantRank:  -1,
			wantValid: false,
		},
		{
			name:      "whitespace trimmed",
			input:     " v1 ",
			wantName:  "v1",
			wantRank:  1,
			wantValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.input)
			if got.Name != tt.wantName {
				t.Errorf("Parse().Name = %v, want %v", got.Name, tt.wantName)
			}
			if got.Rank != tt.wantRank {
				t.Errorf("Parse().Rank = %v, want %v", got.Rank, tt.wantRank)
			}
			if got.Valid != tt.wantValid {
				t.Errorf("Parse().Valid = %v, want %v", got.Valid, tt.wantValid)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid unversioned", input: "_unversioned", wantErr: false},
		{name: "valid v1", input: "v1", wantErr: false},
		{name: "valid v2", input: "v2", wantErr: false},
		{name: "valid v10", input: "v10", wantErr: false},
		{name: "invalid v0", input: "v0", wantErr: true},
		{name: "invalid no prefix", input: "1", wantErr: true},
		{name: "invalid leading zero", input: "v01", wantErr: true},
		{name: "invalid letters", input: "vx", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIsValid(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"_unversioned", true},
		{"v1", true},
		{"v2", true},
		{"v10", true},
		{"v0", false},
		{"1", false},
		{"v01", false},
		{"vx", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := IsValid(tt.input)
			if got != tt.want {
				t.Errorf("IsValid(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestRank(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"_unversioned", 0},
		{"v1", 1},
		{"v2", 2},
		{"v10", 10},
		{"v99", 99},
		{"invalid", -1},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := Rank(tt.input)
			if got != tt.want {
				t.Errorf("Rank(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsOlder(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{"unversioned older than v1", "_unversioned", "v1", true},
		{"v1 older than v2", "v1", "v2", true},
		{"v1 not older than v1", "v1", "v1", false},
		{"v2 not older than v1", "v2", "v1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsOlder(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("IsOlder(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestSelectOldest(t *testing.T) {
	tests := []struct {
		name     string
		versions []string
		want     string
	}{
		{
			name:     "single version",
			versions: []string{"v1"},
			want:     "v1",
		},
		{
			name:     "unversioned and v1",
			versions: []string{"_unversioned", "v1"},
			want:     "_unversioned",
		},
		{
			name:     "v1 and v2",
			versions: []string{"v1", "v2"},
			want:     "v1",
		},
		{
			name:     "multiple versions",
			versions: []string{"v3", "v1", "v2"},
			want:     "v1",
		},
		{
			name:     "empty list returns default",
			versions: []string{},
			want:     "_unversioned",
		},
		{
			name:     "unversioned wins always",
			versions: []string{"v10", "v5", "_unversioned", "v1"},
			want:     "_unversioned",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SelectOldest(tt.versions)
			if got != tt.want {
				t.Errorf("SelectOldest(%v) = %v, want %v", tt.versions, got, tt.want)
			}
		})
	}
}

func TestIsDefaultForUnversionedCallers(t *testing.T) {
	tests := []struct {
		name        string
		version     string
		allVersions []string
		want        bool
	}{
		{
			name:        "unversioned is default when only unversioned exists",
			version:     "_unversioned",
			allVersions: []string{"_unversioned"},
			want:        true,
		},
		{
			name:        "unversioned is default with v1",
			version:     "_unversioned",
			allVersions: []string{"_unversioned", "v1"},
			want:        true,
		},
		{
			name:        "v1 is default when only v1 exists",
			version:     "v1",
			allVersions: []string{"v1"},
			want:        true,
		},
		{
			name:        "v1 is default when v1 and v2 exist",
			version:     "v1",
			allVersions: []string{"v1", "v2"},
			want:        true,
		},
		{
			name:        "v2 is not default when v1 exists",
			version:     "v2",
			allVersions: []string{"v1", "v2"},
			want:        false,
		},
		{
			name:        "v2 is default when only v2 exists",
			version:     "v2",
			allVersions: []string{"v2"},
			want:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsDefaultForUnversionedCallers(tt.version, tt.allVersions)
			if got != tt.want {
				t.Errorf("IsDefaultForUnversionedCallers(%v, %v) = %v, want %v",
					tt.version, tt.allVersions, got, tt.want)
			}
		})
	}
}

func TestFormatForParameter(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"_unversioned", "_unversioned"},
		{"v1", "v1"},
		{" v1 ", "v1"},
		{"", "_unversioned"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := FormatForParameter(tt.input)
			if got != tt.want {
				t.Errorf("FormatForParameter(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

package version

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Default version for fragments that don't declare x-api-version
const Default = "_unversioned"

// versionGrammar is the regex for valid version strings: ^v[1-9][0-9]*$ or _unversioned
var versionGrammar = regexp.MustCompile(`^v[1-9][0-9]*$`)

// Version represents an API version with its rank for selection ordering
type Version struct {
	Name  string // The version identifier (e.g., "v1", "v2", "_unversioned")
	Rank  int    // Sort order: lower = older (served by default when no version specified)
	Valid bool   // Whether this version matches the grammar
}

// Parse parses a version string and returns a Version object
func Parse(v string) Version {
	v = strings.TrimSpace(v)
	if v == "" {
		return Version{Name: Default, Rank: 0, Valid: true}
	}
	if v == Default {
		return Version{Name: v, Rank: 0, Valid: true}
	}
	if versionGrammar.MatchString(v) {
		// Extract numeric part from "vN" to compute rank
		numStr := v[1:] // Skip the 'v'
		num, err := strconv.Atoi(numStr)
		if err == nil && num >= 1 {
			// Rank starts at 1 for v1, 2 for v2, etc.
			// _unversioned is rank 0, so it's always oldest
			return Version{Name: v, Rank: num, Valid: true}
		}
	}
	return Version{Name: v, Rank: -1, Valid: false}
}

// Validate checks if a version string is valid according to the grammar
func Validate(v string) error {
	parsed := Parse(v)
	if !parsed.Valid {
		return fmt.Errorf("invalid API version %q: must match grammar ^v[1-9][0-9]*$ or be %q", v, Default)
	}
	return nil
}

// IsValid checks if a version string is valid
func IsValid(v string) bool {
	return Parse(v).Valid
}

// Rank returns the sort order rank for a version string
// Lower ranks are older and selected by default when no version is specified
func Rank(v string) int {
	return Parse(v).Rank
}

// IsOlder returns true if version a is older than version b
func IsOlder(a, b string) bool {
	return Rank(a) < Rank(b)
}

// SelectOldest selects the oldest version from a list of version strings
func SelectOldest(versions []string) string {
	if len(versions) == 0 {
		return Default
	}
	oldest := versions[0]
	for _, v := range versions[1:] {
		if IsOlder(v, oldest) {
			oldest = v
		}
	}
	return oldest
}

// IsDefaultForUnversionedCallers returns true if the version is the one served
// to callers that don't specify X-SEAM-API-Version
// This is always the oldest still-served version
func IsDefaultForUnversionedCallers(v string, allVersions []string) bool {
	return v == SelectOldest(allVersions)
}

// FormatForParameter formats a version for use in a ?version= query parameter
func FormatForParameter(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return Default
	}
	return v
}

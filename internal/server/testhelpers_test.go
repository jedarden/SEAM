package server

import "strings"

// containsString reports whether substr appears anywhere in s.
//
// This helper is shared by several *_test.go files in this package. It used to
// be defined privately — with three different bodies — in
// brownout_middleware_test.go, loop_guard_test.go and route_table_test.go,
// which made the whole package fail to compile with "containsString
// redeclared in this block" and blocked every test in it.
func containsString(s, substr string) bool {
	return strings.Contains(s, substr)
}

//go:build !race

package server

// raceEnabled reports whether the binary was built with -race. See
// race_enabled.go.
const raceEnabled = false

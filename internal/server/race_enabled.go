//go:build race

package server

// raceEnabled reports whether the binary was built with -race. The race
// detector's instrumentation overhead (routinely 2-20x per operation, and
// non-uniform across payload sizes) makes absolute or relative wall-clock
// latency assertions meaningless under it - tests that assert a performance
// budget should skip themselves when this is true rather than assert
// against numbers the instrumentation itself distorts.
const raceEnabled = true

package benches

import (
	"testing"
)

// BenchmarkExample is a placeholder benchmark demonstrating the basic structure.
// Replace this with actual benchmarks as the project grows.
func BenchmarkExample(b *testing.B) {
	// Setup code that should NOT be benchmarked goes here
	// ...

	// Reset the timer before the actual benchmark loop
	b.ResetTimer()

	// The actual benchmark - this will be run b.N times
	for i := 0; i < b.N; i++ {
		// Code to benchmark goes here
		// Example: dummy operation
		_ = i * 2
	}
}

// BenchmarkWithAllocation demonstrates how to measure memory allocations
func BenchmarkWithAllocation(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Code that allocates memory
		data := make([]byte, 1024)
		_ = data
	}
}

// BenchmarkParallel demonstrates parallel benchmark execution
func BenchmarkParallel(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			// Code to benchmark in parallel
			_ = make([]byte, 128)
		}
	})
}

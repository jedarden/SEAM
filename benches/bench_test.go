package benches

import (
	"sync/atomic"
	"testing"
)

var benchmarkSink atomic.Pointer[byte]

// BenchmarkWithAllocation demonstrates how to measure memory allocations.
func BenchmarkWithAllocation(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data := make([]byte, 1024)
		benchmarkSink.Store(&data[0])
	}
}

// BenchmarkParallel demonstrates parallel benchmark execution.
func BenchmarkParallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			data := make([]byte, 128)
			benchmarkSink.Store(&data[0])
		}
	})
}

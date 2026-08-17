package benches

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ardenone/seam/internal/server"
)

var benchmarkSink atomic.Pointer[byte]

// BenchmarkProxyForwarding measures the core proxy operation with realistic
// request and response bodies. The upstream server is outside the timed loop,
// so the result includes request construction, forwarding, and response copy.
func BenchmarkProxyForwarding(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"status\":\"ok\"}"))
	}))
	defer upstream.Close()

	proxy, err := server.NewReverseProxy(upstream.URL)
	if err != nil {
		b.Fatalf("create proxy: %v", err)
	}

	cases := []struct {
		name string
		body []byte
	}{
		{name: "GET", body: nil},
		{name: "POST-small", body: []byte("{\"message\":\"hello\"}")},
		{name: "POST-medium", body: bytes.Repeat([]byte("x"), 4*1024)},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				method := http.MethodGet
				if tc.body != nil {
					method = http.MethodPost
				}
				req := httptest.NewRequest(method, "/proxy/benchmark?iteration=1", bytes.NewReader(tc.body))
				w := httptest.NewRecorder()
				proxy.ServeHTTP(w, req)
				if w.Code != http.StatusOK {
					b.Fatalf("expected status 200, got %d", w.Code)
				}
			}
		})
	}
}

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

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// BenchmarkCaptureBaseline benchmarks proxy performance without capture
func BenchmarkCaptureBaseline(b *testing.B) {
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	cm := NewCaptureMiddleware(b.TempDir(), "test-service", "test-incumbent", false)
	// Capture middleware starts enabled; disable it explicitly for the baseline.
	cm.Disable()
	handler := cm.Wrap(nextHandler)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}
}

// BenchmarkCaptureEnabled benchmarks proxy performance with capture enabled
func BenchmarkCaptureEnabled(b *testing.B) {
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	cm := NewCaptureMiddleware(b.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()
	handler := cm.Wrap(nextHandler)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}
}

// BenchmarkCaptureWithSmallPayload benchmarks capture with small payload
func BenchmarkCaptureWithSmallPayload(b *testing.B) {
	requestBody := []byte(`{"test":"data"}`)
	responseBody := []byte(`{"result":"success"}`)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo back request details
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(responseBody)
	})

	cm := NewCaptureMiddleware(b.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()
	handler := cm.Wrap(nextHandler)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/api/test", bytes.NewReader(requestBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}
}

// BenchmarkCaptureWithMediumPayload benchmarks capture with medium payload (10KB)
func BenchmarkCaptureWithMediumPayload(b *testing.B) {
	requestBody := bytes.Repeat([]byte("x"), 10*1024)  // 10 KB
	responseBody := bytes.Repeat([]byte("y"), 50*1024) // 50 KB

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read request body
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		w.Write(responseBody)
	})

	cm := NewCaptureMiddleware(b.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()
	handler := cm.Wrap(nextHandler)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/api/test", bytes.NewReader(requestBody))
		req.Header.Set("Content-Type", "application/octet-stream")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}
}

// BenchmarkCaptureWithLargePayload benchmarks capture with large payload (100KB)
func BenchmarkCaptureWithLargePayload(b *testing.B) {
	requestBody := bytes.Repeat([]byte("x"), 100*1024)  // 100 KB
	responseBody := bytes.Repeat([]byte("y"), 500*1024) // 500 KB

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read request body
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		w.Write(responseBody)
	})

	cm := NewCaptureMiddleware(b.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()
	handler := cm.Wrap(nextHandler)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/api/test", bytes.NewReader(requestBody))
		req.Header.Set("Content-Type", "application/octet-stream")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}
}

// BenchmarkCaptureWithVeryLargePayload benchmarks capture with very large payload (1MB)
func BenchmarkCaptureWithVeryLargePayload(b *testing.B) {
	requestBody := bytes.Repeat([]byte("x"), 1024*1024)    // 1 MB
	responseBody := bytes.Repeat([]byte("y"), 5*1024*1024) // 5 MB

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read request body
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		w.Write(responseBody)
	})

	cm := NewCaptureMiddleware(b.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()
	handler := cm.Wrap(nextHandler)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/api/test", bytes.NewReader(requestBody))
		req.Header.Set("Content-Type", "application/octet-stream")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}
}

// BenchmarkCaptureWithHeaders benchmarks capture with various headers
func BenchmarkCaptureWithHeaders(b *testing.B) {
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom-Header", "custom-value")
		w.Header().Set("X-Request-Id", "req-123")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	cm := NewCaptureMiddleware(b.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()
	handler := cm.Wrap(nextHandler)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		req.Header.Set("Authorization", "Bearer token123")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "TestAgent/1.0")
		req.Header.Set("X-Custom-Header", "custom-value")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}
}

// BenchmarkCaptureParallel benchmarks concurrent capture operations
func BenchmarkCaptureParallel(b *testing.B) {
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	cm := NewCaptureMiddleware(b.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()
	handler := cm.Wrap(nextHandler)

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req := httptest.NewRequest("GET", "/api/test", nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				b.Fatalf("expected status 200, got %d", w.Code)
			}
		}
	})
}

// BenchmarkCaptureSaveOperation benchmarks the save operation
func BenchmarkCaptureSaveOperation(b *testing.B) {
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		b.StopTimer()

		corpusDir := b.TempDir()
		cm := NewCaptureMiddleware(corpusDir, "test-service", "test-incumbent", false)
		cm.Enable()
		handler := cm.Wrap(nextHandler)

		// Capture 1000 entries
		for j := 0; j < 1000; j++ {
			req := httptest.NewRequest("GET", fmt.Sprintf("/api/test%d", j), nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
		}

		b.StartTimer()

		// Benchmark the save operation
		if err := cm.Save(); err != nil {
			b.Fatalf("Save failed: %v", err)
		}

		b.StopTimer()
	}
}

// BenchmarkCaptureLoadOperation benchmarks the load operation
func BenchmarkCaptureLoadOperation(b *testing.B) {
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		b.StopTimer()

		corpusDir := b.TempDir()
		cm1 := NewCaptureMiddleware(corpusDir, "test-service", "test-incumbent", false)
		cm1.Enable()
		handler := cm1.Wrap(nextHandler)

		// Capture and save 1000 entries
		for j := 0; j < 1000; j++ {
			req := httptest.NewRequest("GET", fmt.Sprintf("/api/test%d", j), nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
		}

		if err := cm1.Save(); err != nil {
			b.Fatalf("Save failed: %v", err)
		}

		b.StartTimer()

		// Benchmark the load operation
		cm2 := NewCaptureMiddleware(corpusDir, "test-service", "test-incumbent", false)
		if err := cm2.Load(); err != nil {
			b.Fatalf("Load failed: %v", err)
		}

		b.StopTimer()
	}
}

// BenchmarkCaptureWithJSONEncoding benchmarks capture with JSON encoding/decoding
func BenchmarkCaptureWithJSONEncoding(b *testing.B) {
	complexJSON := strings.Repeat(`{"item":"value","nested":{"key":"value","array":[1,2,3]}},`, 100)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Parse JSON
		var data []map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&data)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(complexJSON))
	})

	cm := NewCaptureMiddleware(b.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()
	handler := cm.Wrap(nextHandler)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/api/test", strings.NewReader(complexJSON))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}
}

// BenchmarkCaptureMemoryUsage estimates memory usage per captured entry
func BenchmarkCaptureMemoryUsage(b *testing.B) {
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	cm := NewCaptureMiddleware(b.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()
	handler := cm.Wrap(nextHandler)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/test%d", i), nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}

	b.ReportMetric(float64(cm.GetEntryCount()), "entries")
}

// BenchmarkCaptureOverheadByMethod benchmarks capture overhead by HTTP method
func BenchmarkCaptureOverheadByMethod(b *testing.B) {
	methods := []string{"GET", "POST", "PUT", "PATCH", "DELETE"}

	for _, method := range methods {
		b.Run(method, func(b *testing.B) {
			nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"result":"ok"}`))
			})

			cm := NewCaptureMiddleware(b.TempDir(), "test-service", "test-incumbent", false)
			cm.Enable()
			handler := cm.Wrap(nextHandler)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				var body io.Reader
				if method == "POST" || method == "PUT" || method == "PATCH" {
					body = strings.NewReader(`{"test":"data"}`)
				}

				req := httptest.NewRequest(method, "/api/test", body)
				if body != nil {
					req.Header.Set("Content-Type", "application/json")
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)

				if w.Code != http.StatusOK {
					b.Fatalf("expected status 200, got %d", w.Code)
				}
			}
		})
	}
}

// BenchmarkCaptureWithQueryParams benchmarks capture with various query parameters
func BenchmarkCaptureWithQueryParams(b *testing.B) {
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result":"ok"}`))
	})

	cm := NewCaptureMiddleware(b.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()
	handler := cm.Wrap(nextHandler)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		url := fmt.Sprintf("/api/test?param1=value1&param2=value2&page=%d&limit=10&sort=desc", i%100)
		req := httptest.NewRequest("GET", url, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}
}

// BenchmarkCaptureDisabled verifies minimal overhead when capture is disabled
func BenchmarkCaptureDisabled(b *testing.B) {
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	cm := NewCaptureMiddleware(b.TempDir(), "test-service", "test-incumbent", false)
	// Explicitly disable capture
	cm.Disable()
	handler := cm.Wrap(nextHandler)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}
}

// BenchmarkCaptureThroughputSaturation measures throughput at different saturation points
func BenchmarkCaptureThroughputSaturation(b *testing.B) {
	targetRPS := []int{100, 500, 1000, 5000}

	for _, rps := range targetRPS {
		b.Run(fmt.Sprintf("RPS-%d", rps), func(b *testing.B) {
			nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"status":"ok"}`))
			})

			cm := NewCaptureMiddleware(b.TempDir(), "test-service", "test-incumbent", false)
			cm.Enable()
			handler := cm.Wrap(nextHandler)

			interval := time.Second / time.Duration(rps)

			b.ResetTimer()

			startTime := time.Now()
			successCount := 0

			for i := 0; i < b.N; i++ {
				// Throttle to target RPS
				elapsed := time.Since(startTime)
				expectedDuration := time.Duration(i) * interval
				if elapsed < expectedDuration {
					time.Sleep(expectedDuration - elapsed)
				}

				req := httptest.NewRequest("GET", "/api/test", nil)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)

				if w.Code == http.StatusOK {
					successCount++
				}
			}

			actualRPS := float64(successCount) / time.Since(startTime).Seconds()
			b.ReportMetric(actualRPS, "req/sec")
		})
	}
}

// BenchmarkCaptureLatencyDistribution measures latency percentiles under load
func BenchmarkCaptureLatencyDistribution(b *testing.B) {
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	cm := NewCaptureMiddleware(b.TempDir(), "test-service", "test-incumbent", false)
	cm.Enable()
	handler := cm.Wrap(nextHandler)

	b.ResetTimer()

	latencies := make([]time.Duration, b.N)
	for i := 0; i < b.N; i++ {
		start := time.Now()
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		latencies[i] = time.Since(start)

		if w.Code != http.StatusOK {
			b.Fatalf("expected status 200, got %d", w.Code)
		}
	}

	b.StopTimer()

	// Calculate percentiles
	p50, p95, p99 := percentiles(latencies)

	b.ReportMetric(float64(p50.Microseconds()), "µs_p50")
	b.ReportMetric(float64(p95.Microseconds()), "µs_p95")
	b.ReportMetric(float64(p99.Microseconds()), "µs_p99")
}

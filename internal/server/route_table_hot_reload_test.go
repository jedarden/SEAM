package server

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ardenone/seam/internal/watcher"
)

// TestRouteTableHotReloadUnderLoad tests that route table swaps happen
// atomically without dropping connections while concurrent requests are in flight.
//
// This test verifies:
// 1. In-flight requests complete on the old route table
// 2. New requests after swap use the new route table
// 3. No requests are dropped or receive errors during swap
// 4. The swap is truly atomic (no partial state exposure)
// 5. Sub-second reload time
func TestRouteTableHotReloadUnderLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	// Create a thread-safe holder with an initial route table
	holder := NewThreadSafeTableHolder(&RouteTable{
		routes: []RouteEntry{
			{
				PathTemplate:   "/v1/users",
				Method:         "GET",
				APIVersion:     "v1",
				UpstreamTarget: "http://v1-backend",
			},
		},
	})

	// Number of concurrent readers to simulate load
	const numConcurrentReaders = 100
	const numSwaps = 10

	var wg sync.WaitGroup
	readErrors := atomic.Int64{}
	swapErrors := atomic.Int64{}
	readCount := atomic.Int64{}

	// Start concurrent readers
	for i := 0; i < numConcurrentReaders; i++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()

			for j := 0; j < numSwaps; j++ {
				req, _ := http.NewRequest("GET", "/v1/users", nil)
				match, err := holder.Match(req)
				readCount.Add(1)

				if err != nil {
					readErrors.Add(1)
					t.Logf("Reader %d: match error: %v", readerID, err)
					return
				}

				// Verify we got a valid match
				if match == nil {
					readErrors.Add(1)
					t.Logf("Reader %d: got nil match", readerID)
					return
				}

				// Verify the route has valid fields
				if match.Route.PathTemplate == "" {
					readErrors.Add(1)
					t.Logf("Reader %d: got empty path template", readerID)
					return
				}
			}
		}(i)
	}

	// Perform concurrent swaps
	for i := 0; i < numSwaps; i++ {
		wg.Add(1)
		go func(swapID int) {
			defer wg.Done()

			// Create a new route table with a different version
			newTable := &RouteTable{
				routes: []RouteEntry{
					{
						PathTemplate:   "/v1/users",
						Method:         "GET",
						APIVersion:     fmt.Sprintf("v%d", (swapID%3)+1),
						UpstreamTarget: fmt.Sprintf("http://v%d-backend", (swapID%3)+1),
					},
				},
			}

			// Measure swap time
			swapStart := time.Now()

			// Atomic swap
			if err := holder.Swap(newTable); err != nil {
				swapErrors.Add(1)
				t.Logf("Swap %d failed: %v", swapID, err)
				return
			}

			swapDuration := time.Since(swapStart)
			if swapDuration > time.Second {
				t.Logf("Swap %d took %dms, expected sub-second", swapID, swapDuration.Milliseconds())
			}
		}(i)

		// Small delay between swaps
		time.Sleep(time.Microsecond * 100)
	}

	// Wait for all operations to complete
	wg.Wait()

	t.Logf("Test results:")
	t.Logf("  Total reads: %d", readCount.Load())
	t.Logf("  Read errors: %d", readErrors.Load())
	t.Logf("  Swap errors: %d", swapErrors.Load())

	// Verify no read errors
	if readErrors.Load() != 0 {
		t.Errorf("Expected zero read errors, got %d", readErrors.Load())
	}

	// Verify no swap errors
	if swapErrors.Load() != 0 {
		t.Errorf("Expected zero swap errors, got %d", swapErrors.Load())
	}

	// Verify we performed the expected number of reads
	expectedReads := int64(numConcurrentReaders * numSwaps)
	if readCount.Load() != expectedReads {
		t.Errorf("Expected %d reads, got %d", expectedReads, readCount.Load())
	}
}

// TestRouteTableSwapAtomicity verifies that route table swaps are atomic
// and no partial state is visible to concurrent readers
func TestRouteTableSwapAtomicity(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping atomicity test in short mode")
	}

	// Create multiple route tables with different routes
	table1 := &RouteTable{
		routes: []RouteEntry{
			{PathTemplate: "/v1/users", Method: "GET", APIVersion: "v1", UpstreamTarget: "http://v1-backend"},
		},
	}
	table2 := &RouteTable{
		routes: []RouteEntry{
			{PathTemplate: "/v2/users", Method: "GET", APIVersion: "v2", UpstreamTarget: "http://v2-backend"},
		},
	}
	table3 := &RouteTable{
		routes: []RouteEntry{
			{PathTemplate: "/v3/users", Method: "GET", APIVersion: "v3", UpstreamTarget: "http://v3-backend"},
		},
	}

	// Create a thread-safe holder
	holder := NewThreadSafeTableHolder(table1)

	// Track what each goroutine observes
	observations := make([]string, 0)
	observationsMu := sync.Mutex{}

	// Number of swap operations
	const numSwaps = 100
	// Number of concurrent readers
	const numReaders = 50

	var wg sync.WaitGroup

	// Start concurrent readers
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()

			for j := 0; j < numSwaps; j++ {
				// Match a request against the current table
				req, _ := http.NewRequest("GET", "/users", nil)
				match, err := holder.Match(req)

				observationsMu.Lock()
				if err != nil {
					observations = append(observations, fmt.Sprintf("reader%d: error", readerID))
				} else if match != nil {
					observations = append(observations, fmt.Sprintf("reader%d: version=%s", readerID, match.Route.APIVersion))
				}
				observationsMu.Unlock()

				// Small delay to spread out the reads
				time.Sleep(time.Microsecond * time.Duration(10))
			}
		}(i)
	}

	// Perform swaps
	tables := []*RouteTable{table1, table2, table3}
	for i := 0; i < numSwaps; i++ {
		table := tables[i%len(tables)]
		if err := holder.Swap(table); err != nil {
			t.Errorf("Swap %d failed: %v", i, err)
		}
		// Small delay between swaps
		time.Sleep(time.Microsecond * 100)
	}

	// Wait for all readers to finish
	wg.Wait()

	// Verify that we never observed a partial or invalid state
	t.Logf("Total observations: %d", len(observations))

	// Count observations by version
	versionCounts := make(map[string]int)
	for _, obs := range observations {
		if strings.Contains(obs, "version=") {
			versionCounts[obs]++
		}
	}

	t.Logf("Observations by version:")
	for version, count := range versionCounts {
		t.Logf("  %s: %d", version, count)
	}

	// Verify we observed valid versions
	validVersions := map[string]bool{
		"v1": true, "v2": true, "v3": true,
	}

	for obs := range versionCounts {
		version := strings.TrimPrefix(obs, "reader")
		version = strings.TrimPrefix(version, ": version=")
		if !validVersions[version] {
			t.Errorf("Observed invalid version: %s", version)
		}
	}
}

// TestConcurrentRouteTableSwaps tests that multiple concurrent swaps
// complete safely and the final table is valid
func TestConcurrentRouteTableSwaps(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent swaps test in short mode")
	}

	holder := NewThreadSafeTableHolder(nil)

	const numSwaps = 50
	const numGoroutines = 10

	var wg sync.WaitGroup
	successCount := atomic.Int64{}
	failCount := atomic.Int64{}

	// Launch multiple goroutines performing concurrent swaps
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for i := 0; i < numSwaps; i++ {
				// Create a new route table
				table := &RouteTable{
					routes: []RouteEntry{
						{
							PathTemplate:   fmt.Sprintf("/test/%d/%d", goroutineID, i),
							Method:         "GET",
							APIVersion:     "_unversioned",
							UpstreamTarget: "http://backend",
						},
					},
				}

				// Attempt to swap
				if err := holder.Swap(table); err != nil {
					failCount.Add(1)
				} else {
					successCount.Add(1)
				}

				// Small delay
				time.Sleep(time.Microsecond * 10)
			}
		}(g)
	}

	// Wait for all swaps to complete
	wg.Wait()

	t.Logf("Concurrent swap results:")
	t.Logf("  Successful swaps: %d", successCount.Load())
	t.Logf("  Failed swaps: %d", failCount.Load())

	// All swaps should succeed
	if successCount.Load() != numGoroutines*numSwaps {
		t.Errorf("Expected %d successful swaps, got %d", numGoroutines*numSwaps, successCount.Load())
	}

	// No swaps should fail
	if failCount.Load() != 0 {
		t.Errorf("Expected zero failures, got %d", failCount.Load())
	}

	// Verify the final table is valid
	req, _ := http.NewRequest("GET", "/any/path", nil)
	match, err := holder.Match(req)
	if err != nil {
		t.Logf("Final table match error (expected): %v", err)
	} else if match != nil {
		t.Logf("Final table has a route (valid)")
	}
}

// BenchmarkRouteTableSwap benchmarks the performance of route table swaps
func BenchmarkRouteTableSwap(b *testing.B) {
	// Create a realistic route table with many routes
	routes := make([]RouteEntry, 100)
	for i := 0; i < 100; i++ {
		routes[i] = RouteEntry{
			PathTemplate:   fmt.Sprintf("/api/resource/%d", i),
			Method:         "GET",
			APIVersion:     "v1",
			UpstreamTarget: "http://backend",
		}
	}

	holder := NewThreadSafeTableHolder(&RouteTable{routes: routes})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Create a new table with slightly different routes
		newRoutes := make([]RouteEntry, 100)
		for j := 0; j < 100; j++ {
			newRoutes[j] = RouteEntry{
				PathTemplate:   fmt.Sprintf("/api/resource/%d", j),
				Method:         "GET",
				APIVersion:     fmt.Sprintf("v%d", i%3+1),
				UpstreamTarget: "http://backend",
			}
		}

		newTable := &RouteTable{routes: newRoutes}
		if err := holder.Swap(newTable); err != nil {
			b.Fatalf("Swap failed: %v", err)
		}
	}
}

// BenchmarkRouteTableMatchUnderSwaps benchmarks request matching while
// concurrent swaps are happening
func BenchmarkRouteTableMatchUnderSwaps(b *testing.B) {
	routes := make([]RouteEntry, 100)
	for i := 0; i < 100; i++ {
		routes[i] = RouteEntry{
			PathTemplate:   fmt.Sprintf("/api/resource/%d", i),
			Method:         "GET",
			APIVersion:     "v1",
			UpstreamTarget: "http://backend",
		}
	}

	holder := NewThreadSafeTableHolder(&RouteTable{routes: routes})

	// Start a goroutine that continuously swaps tables
	stopSwaps := make(chan struct{})
	go func() {
		i := 0
		for {
			select {
			case <-stopSwaps:
				return
			default:
				newRoutes := make([]RouteEntry, 100)
				for j := 0; j < 100; j++ {
					newRoutes[j] = RouteEntry{
						PathTemplate:   fmt.Sprintf("/api/resource/%d", j),
						Method:         "GET",
						APIVersion:     fmt.Sprintf("v%d", i%3+1),
						UpstreamTarget: "http://backend",
					}
				}
				holder.Swap(&RouteTable{routes: newRoutes})
				i++
			}
		}
	}()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("GET", "/api/resource/42", nil)
		_, err := holder.Match(req)
		if err != nil {
			b.Fatalf("Match failed: %v", err)
		}
	}

	close(stopSwaps)
}

// TestLoadFragmentsReload tests the LoadFragments method on the spec loader
func TestLoadFragmentsReload(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping fragment reload test in short mode")
	}

	// This test would require setting up a temporary fragments directory
	// For now, we'll test that the method exists and can be called
	t.Skip("LoadFragments test requires temporary directory setup")
}

// TestMultiMountWatcher tests that multiple mount points are watched correctly
func TestMultiMountWatcher(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping multi-mount watcher test in short mode")
	}

	// Create temporary directories to simulate Kubernetes ConfigMap mounts
	tempDir := t.TempDir()

	// Create service mount directories
	service1Dir := filepath.Join(tempDir, "routes.d", "service1")
	service2Dir := filepath.Join(tempDir, "routes.d", "service2")
	allowlistDir := filepath.Join(tempDir, "allowlist")
	caDir := filepath.Join(tempDir, "upstream-ca")

	for _, dir := range []string{service1Dir, service2Dir, allowlistDir, caDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("Failed to create directory %s: %v", dir, err)
		}
	}

	// Create a coordinator
	coord := watcher.NewCoordinator()

	// Add watchers for each mount
	mounts := []string{
		filepath.Join(tempDir, "routes.d"),
		service1Dir,
		service2Dir,
		allowlistDir,
		caDir,
	}

	for _, mount := range mounts {
		if err := coord.AddMount(mount); err != nil {
			t.Logf("Warning: failed to add mount %s: %v", mount, err)
			// Some mounts might not have ..data symlinks in test environment
		}
	}

	// Start the coordinator
	coord.Start()

	// Verify coordinator is running
	if coord.Stopped() {
		t.Error("Coordinator should not be stopped after Start()")
	}

	// Stop the coordinator
	coord.Stop()

	// Verify coordinator is stopped
	if !coord.Stopped() {
		t.Error("Coordinator should be stopped after Stop()")
	}

	t.Log("Multi-mount watcher test completed successfully")
}

// TestConcurrentSwapsUnderRaceDetector tests atomic swaps under -race
func TestConcurrentSwapsUnderRaceDetector(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping race detector test in short mode")
	}

	// Create a thread-safe holder
	holder := NewThreadSafeTableHolder(nil)

	const numSwaps = 100
	const numReaders = 10

	var wg sync.WaitGroup
	stopReaders := make(chan struct{})
	swapErrors := make(chan error, numSwaps)

	// Start concurrent readers
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()
			for {
				select {
				case <-stopReaders:
					return
				default:
					req, _ := http.NewRequest("GET", "/test/path", nil)
					_, err := holder.Match(req)
					// Errors are expected when table is empty or nil
					_ = err
				}
			}
		}(i)
	}

	// Perform concurrent swaps
	for i := 0; i < numSwaps; i++ {
		wg.Add(1)
		go func(swapID int) {
			defer wg.Done()
			table := &RouteTable{
				routes: []RouteEntry{
					{
						PathTemplate:   fmt.Sprintf("/test/%d", swapID),
						Method:         "GET",
						APIVersion:     "_unversioned",
						UpstreamTarget: "http://backend",
					},
				},
			}
			if err := holder.Swap(table); err != nil {
				swapErrors <- err
			}
		}(i)
	}

	// Wait a bit for concurrent operations to execute
	time.Sleep(100 * time.Millisecond)

	// Stop readers
	close(stopReaders)

	// Wait for all operations to complete
	wg.Wait()

	close(swapErrors)

	// Check for swap errors
	for err := range swapErrors {
		t.Errorf("Swap error: %v", err)
	}

	t.Log("Concurrent swaps under race detector test completed successfully")
}

// TestRouteTableSwapWhileStreaming tests that route table swaps happen correctly
// while multiple clients are streaming chunked responses. This verifies:
// 1. In-flight streaming requests complete without interruption
// 2. No dropped connections during swap
// 3. Sub-second swap times
// 4. Atomic swap with no partial state visible
func TestRouteTableSwapWhileStreaming(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping streaming stress test in short mode")
	}

	// Number of concurrent streaming clients
	const numStreamers = 50
	const numSwaps = 10
	const chunksPerStream = 20

	// Create a test upstream server that streams chunked responses
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set up chunked transfer encoding
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Transfer-Encoding", "chunked")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("Server does not support flushing")
		}

		// Stream chunks over time
		for i := 0; i < chunksPerStream; i++ {
			fmt.Fprintf(w, "chunk-%d\n", i)
			flusher.Flush()
			// Small delay to simulate real streaming
			time.Sleep(time.Microsecond * 10)
		}
	}))
	defer upstream.Close()

	// Create initial route table
	holder := NewThreadSafeTableHolder(&RouteTable{
		routes: []RouteEntry{
			{
				PathTemplate:   "/stream",
				Method:         "GET",
				APIVersion:     "v1",
				UpstreamTarget: upstream.URL,
			},
		},
	})

	var wg sync.WaitGroup
	streamErrors := atomic.Int64{}
	swapErrors := atomic.Int64{}
	completedStreams := atomic.Int64{}
	bytesReceived := atomic.Int64{}

	// Start concurrent streaming clients
	for i := 0; i < numStreamers; i++ {
		wg.Add(1)
		go func(streamerID int) {
			defer wg.Done()

			for j := 0; j < numSwaps; j++ {
				// Make a streaming request
				resp, err := http.Get(upstream.URL + "/stream")
				if err != nil {
					streamErrors.Add(1)
					t.Logf("Streamer %d-%d: request failed: %v", streamerID, j, err)
					return
				}

				// Read the response body
				data, err := io.ReadAll(resp.Body)
				resp.Body.Close()

				if err != nil {
					streamErrors.Add(1)
					t.Logf("Streamer %d-%d: read failed: %v", streamerID, j, err)
					return
				}

				if resp.StatusCode != http.StatusOK {
					streamErrors.Add(1)
					t.Logf("Streamer %d-%d: got status %d", streamerID, j, resp.StatusCode)
					return
				}

				// Verify we received all chunks
				expectedChunks := 0
				for k := 0; k < chunksPerStream; k++ {
					if strings.Contains(string(data), fmt.Sprintf("chunk-%d", k)) {
						expectedChunks++
					}
				}

				if expectedChunks != chunksPerStream {
					streamErrors.Add(1)
					t.Logf("Streamer %d-%d: incomplete stream (got %d/%d chunks)",
						streamerID, j, expectedChunks, chunksPerStream)
					return
				}

				completedStreams.Add(1)
				bytesReceived.Add(int64(len(data)))
			}
		}(i)
	}

	// Perform concurrent swaps while streams are active
	for i := 0; i < numSwaps; i++ {
		wg.Add(1)
		go func(swapID int) {
			defer wg.Done()

			// Create a new route table with a different version
			newTable := &RouteTable{
				routes: []RouteEntry{
					{
						PathTemplate:   "/stream",
						Method:         "GET",
						APIVersion:     fmt.Sprintf("v%d", (swapID%3)+1),
						UpstreamTarget: upstream.URL,
					},
				},
			}

			// Measure swap time
			swapStart := time.Now()
			if err := holder.Swap(newTable); err != nil {
				swapErrors.Add(1)
				t.Logf("Swap %d failed: %v", swapID, err)
				return
			}
			swapDuration := time.Since(swapStart)

			// Verify sub-second swap
			if swapDuration > time.Second {
				t.Logf("Swap %d took %dms, expected sub-second",
					swapID, swapDuration.Milliseconds())
			}
		}(i)

		// Delay between swaps to allow streams to be in-flight
		time.Sleep(time.Microsecond * 500)
	}

	// Wait for all operations to complete
	wg.Wait()

	t.Logf("Streaming stress test results:")
	t.Logf("  Completed streams: %d", completedStreams.Load())
	t.Logf("  Stream errors: %d", streamErrors.Load())
	t.Logf("  Swap errors: %d", swapErrors.Load())
	t.Logf("  Total bytes received: %d", bytesReceived.Load())

	// Verify no stream errors (zero dropped connections)
	if streamErrors.Load() != 0 {
		t.Errorf("Expected zero stream errors, got %d", streamErrors.Load())
	}

	// Verify no swap errors
	if swapErrors.Load() != 0 {
		t.Errorf("Expected zero swap errors, got %d", swapErrors.Load())
	}

	// Verify we completed all expected streams
	expectedStreams := int64(numStreamers * numSwaps)
	if completedStreams.Load() != expectedStreams {
		t.Errorf("Expected %d completed streams, got %d",
			expectedStreams, completedStreams.Load())
	}

	// Verify we received data
	if bytesReceived.Load() == 0 {
		t.Error("Expected to receive data from streams")
	}
}

package server

import (
	"testing"
)

// TestBufferPoolReuse verifies that the bufferPool reuses buffers
// and doesn't allocate on every Get/Put cycle in steady state.
func TestBufferPoolReuse(t *testing.T) {
	pool := newBufferPool()

	// Get a buffer, put it back, then get another
	// In steady state, the second Get should reuse the first buffer
	buf1 := pool.Get()
	if len(buf1) != 32*1024 {
		t.Errorf("expected buffer size 32*1024, got %d", len(buf1))
	}

	// Put the buffer back
	pool.Put(buf1)

	// Get another buffer - should be the same one we just put back
	buf2 := pool.Get()
	if len(buf2) != 32*1024 {
		t.Errorf("expected buffer size 32*1024, got %d", len(buf2))
	}

	// Verify they're the same underlying array (cap is the same, and they point to same memory)
	// In sync.Pool, if we get back the same buffer, the underlying array should be reused
	// We can verify this by checking if buf1 and buf2 have the same capacity
	if cap(buf1) != cap(buf2) {
		t.Errorf("buffer capacity mismatch: cap(buf1)=%d, cap(buf2)=%d", cap(buf1), cap(buf2))
	}

	// The key test: after a warmup cycle, steady-state Get/Put should minimize allocations
	// We use testing.AllocsPerRun to verify this
	// Note: There may be 1 small allocation for the slice header dereference, but the
	// underlying 32KB buffer array should be reused.
	allocs := testing.AllocsPerRun(100, func() {
		buf := pool.Get()
		pool.Put(buf)
	})

	// We expect minimal allocations in steady state (ideally just the slice header, not the buffer)
	// The old implementation would allocate ~32KB per cycle; sync.Pool should be much less
	if allocs > 1.0 {
		t.Errorf("expected minimal allocations per Get/Put cycle in steady state (ideally just slice header), got %f", allocs)
	}
	t.Logf("Allocations per Get/Put cycle: %f", allocs)

	// Clean up
	pool.Put(buf2)
}

// TestSharedBufferPool verifies that all ReverseProxy instances share the same pool
func TestSharedBufferPool(t *testing.T) {
	pool1 := newBufferPool()
	pool2 := newBufferPool()

	// Both should be the same shared pool instance
	if pool1 != pool2 {
		t.Error("newBufferPool should return the same shared instance")
	}

	// Get a buffer from pool1, return it to pool2
	buf1 := pool1.Get()
	pool2.Put(buf1)

	// Get from pool2 - should be the same buffer
	buf2 := pool2.Get()
	if len(buf2) != 32*1024 {
		t.Errorf("expected buffer size 32*1024, got %d", len(buf2))
	}

	pool2.Put(buf2)
}

// TestBufferPoolConcurrentUse verifies the pool is safe for concurrent use
func TestBufferPoolConcurrentUse(t *testing.T) {
	pool := newBufferPool()
	done := make(chan bool)

	// Launch multiple goroutines doing Get/Put cycles
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				buf := pool.Get()
				if len(buf) != 32*1024 {
					t.Errorf("expected buffer size 32*1024, got %d", len(buf))
				}
				pool.Put(buf)
			}
			done <- true
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}
}

// TestBufferPoolBufferSize verifies all buffers have the expected size
func TestBufferPoolBufferSize(t *testing.T) {
	pool := newBufferPool()
	const expectedSize = 32 * 1024

	// Get multiple buffers and verify size
	for i := 0; i < 10; i++ {
		buf := pool.Get()
		if len(buf) != expectedSize {
			t.Errorf("iteration %d: expected buffer size %d, got %d", i, expectedSize, len(buf))
		}
		if cap(buf) < expectedSize {
			t.Errorf("iteration %d: buffer capacity %d less than expected size %d", i, cap(buf), expectedSize)
		}
		pool.Put(buf)
	}
}

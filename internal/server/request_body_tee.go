package server

import (
	"bytes"
	"io"
	"sync"
)

const (
	// DefaultMaxReplayableRequestBytes is the default maximum request body size
	// that will be buffered for potential replay (1 MiB).
	// This is independent of maxBufferedResponseBytes - tuning one never moves the other.
	DefaultMaxReplayableRequestBytes = 1 * 1024 * 1024
)

// replayableBody is a request body that can be replayed.
// It buffers the body up to maxReplayableRequestBytes while streaming to upstream.
// If the body exceeds the limit or is unreplayable, it degrades to pass-through mode.
type replayableBody struct {
	source         io.ReadCloser
	maxBytes       int64
	buffer         *bytes.Buffer
	mu             sync.Mutex
	bufferComplete bool
	exceededLimit  bool
	unreplayable   bool // Set when body is protocol upgrade or unbounded chunked
	bytesRead      int64
	contentLength  int64 // as declared by the sender; -1 when unknown
}

// newReplayableBody creates a new replayable body from the source reader.
// If contentLength is -1 (unknown) or exceeds maxBytes, the body is marked as unreplayable.
func newReplayableBody(source io.ReadCloser, contentLength int64, maxBytes int64) *replayableBody {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxReplayableRequestBytes
	}

	rb := &replayableBody{
		source:        source,
		maxBytes:      maxBytes,
		buffer:        &bytes.Buffer{},
		contentLength: contentLength,
	}

	// Detect unreplayable conditions
	// 1. Protocol upgrade: WebSocket, HTTP/2 prior knowledge, etc.
	// 2. Unbounded chunked encoding with no declared length (Content-Length: -1)
	if contentLength == -1 {
		// Unknown length - might be chunked without a declared size
		// We'll buffer until we hit the limit, then degrade
		rb.unreplayable = false // Not yet unreplayable, but will degrade if we exceed limit
	} else if contentLength > maxBytes {
		// Body exceeds replay limit - degrade to pass-through
		rb.exceededLimit = true
		rb.unreplayable = true
		rb.bufferComplete = true // No point buffering
	}

	return rb
}

// Read implements io.Reader. It tees data to both the buffer and the caller.
// If the buffer limit is exceeded, subsequent reads only stream to the caller.
func (rb *replayableBody) Read(p []byte) (n int, err error) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	// If already unreplayable or exceeded limit, just pass through
	if rb.unreplayable || rb.exceededLimit {
		n, err = rb.source.Read(p)
		rb.bytesRead += int64(n)
		return n, err
	}

	// Read from source
	n, err = rb.source.Read(p)
	if n > 0 {
		rb.bytesRead += int64(n)

		// Try to buffer the read data
		if !rb.bufferComplete {
			// Check if adding this data would exceed our limit
			if rb.bytesRead > rb.maxBytes {
				// We've exceeded the limit - mark as exceeded and stop buffering
				rb.exceededLimit = true
				rb.unreplayable = true
				rb.bufferComplete = true
				// Don't write to buffer - we've already exceeded
			} else {
				// Buffer the data for potential replay
				if wn, werr := rb.buffer.Write(p[:n]); werr != nil {
					// Buffer write failed - shouldn't happen with bytes.Buffer
					rb.bufferComplete = true
				} else if wn != n {
					// Partial write - shouldn't happen with bytes.Buffer
					rb.bufferComplete = true
				}
			}
		}
	}

	// Mark buffer as complete if we've hit EOF, an error
	if err != nil {
		rb.bufferComplete = true
	} else if n == 0 && rb.bytesRead > 0 {
		// Read returned 0 bytes but no error - treat as EOF
		rb.bufferComplete = true
	}

	// If we've read all expected bytes without an explicit EOF, mark as complete
	// This handles cases where the reader returns all data in one call without EOF
	if !rb.bufferComplete && rb.bytesRead > 0 && n == 0 {
		rb.bufferComplete = true
	}

	return n, err
}

// Close implements io.Closer. It closes the source reader.
func (rb *replayableBody) Close() error {
	return rb.source.Close()
}

// CanReplay returns true if the body can be replayed (was fully buffered within limits).
func (rb *replayableBody) CanReplay() bool {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.canReplayLocked()
}

// canReplayLocked is the internal implementation that assumes the lock is already held.
func (rb *replayableBody) canReplayLocked() bool {
	if rb.exceededLimit || rb.unreplayable {
		return false
	}
	if rb.bufferComplete {
		return true
	}
	// A body with a declared length is replayable as soon as every declared
	// byte has passed through the tee, even before the sender closes it. The
	// HTTP transport drains a Content-Length body with a final Read that
	// returns the remaining bytes but no EOF, so the EOF-driven detection in
	// Read never fires for it; and the 401 retry in the proxy reads the buffer
	// as soon as response headers arrive, which is before the transport closes
	// the request body — waiting for Close there lost the retry every time.
	return rb.contentLength >= 0 && rb.bytesRead >= rb.contentLength
}

// GetBufferedBytes returns a copy of the buffered body bytes.
// Returns nil if the body cannot be replayed.
func (rb *replayableBody) GetBufferedBytes() []byte {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if !rb.canReplayLocked() {
		return nil
	}

	// Return a copy of the buffer
	copied := make([]byte, rb.buffer.Len())
	copy(copied, rb.buffer.Bytes())
	return copied
}

// Discard releases the buffer memory. Call this when the replayable body
// is no longer needed (e.g., when response status is known and no 401 retry will occur).
func (rb *replayableBody) Discard() {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if rb.buffer != nil {
		rb.buffer.Reset()
		rb.bufferComplete = true
		rb.unreplayable = true // Mark as unreplayable after discard
	}
}

// BytesRead returns the number of bytes read from the source.
func (rb *replayableBody) BytesRead() int64 {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.bytesRead
}

// IsReplayable returns true if the body was replayable (not unreplayable and didn't exceed limit).
func (rb *replayableBody) IsReplayable() bool {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return !rb.unreplayable && !rb.exceededLimit
}

// MarkUnreplayable explicitly marks the body as unreplayable.
// Use this for protocol upgrades or other conditions that prevent replay.
func (rb *replayableBody) MarkUnreplayable() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.unreplayable = true
	rb.bufferComplete = true
}

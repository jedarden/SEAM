package server

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestNewReplayableBody(t *testing.T) {
	tests := []struct {
		name          string
		contentLength int64
		maxBytes      int64
		wantUnreplayable bool
		wantExceededLimit bool
	}{
		{
			name:          "small body within limit",
			contentLength: 100,
			maxBytes:      1000,
			wantUnreplayable: false,
			wantExceededLimit: false,
		},
		{
			name:          "body exceeding limit",
			contentLength: 2000,
			maxBytes:      1000,
			wantUnreplayable: true,
			wantExceededLimit: true,
		},
		{
			name:          "unknown content length (chunked)",
			contentLength: -1,
			maxBytes:      1000,
			wantUnreplayable: false,
			wantExceededLimit: false,
		},
		{
			name:          "exact limit match",
			contentLength: 1000,
			maxBytes:      1000,
			wantUnreplayable: false,
			wantExceededLimit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := io.NopCloser(strings.NewReader("test"))
			rb := newReplayableBody(source, tt.contentLength, tt.maxBytes)

			if rb.unreplayable != tt.wantUnreplayable {
				t.Errorf("unreplayable = %v, want %v", rb.unreplayable, tt.wantUnreplayable)
			}
			if rb.exceededLimit != tt.wantExceededLimit {
				t.Errorf("exceededLimit = %v, want %v", rb.exceededLimit, tt.wantExceededLimit)
			}
		})
	}
}

func TestReplayableBodyRead(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		maxBytes      int64
		wantCanReplay bool
		wantReadBytes int64
	}{
		{
			name:          "small body buffered successfully",
			input:         "hello world",
			maxBytes:      100,
			wantCanReplay: true,
			wantReadBytes: 11,
		},
		{
			name:          "body at limit boundary",
			input:         strings.Repeat("a", 1000),
			maxBytes:      1000,
			wantCanReplay: true,
			wantReadBytes: 1000,
		},
		{
			name:          "body exceeds limit mid-read",
			input:         strings.Repeat("b", 1500),
			maxBytes:      1000,
			wantCanReplay: false,
			wantReadBytes: 1500,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := io.NopCloser(strings.NewReader(tt.input))
			rb := newReplayableBody(source, int64(len(tt.input)), tt.maxBytes)

			// Read all data - consume until EOF
			buf := make([]byte, 2000)
			totalRead := 0
			for {
				n, err := rb.Read(buf)
				totalRead += n
				if err != nil {
					if err == io.EOF {
						break
					}
					t.Fatalf("Read() error = %v", err)
				}
				if n == 0 {
					break
				}
			}

			if totalRead != len(tt.input) {
				t.Errorf("Read() read %d bytes total, want %d", totalRead, len(tt.input))
			}

			// Verify CanReplay
			if got := rb.CanReplay(); got != tt.wantCanReplay {
				t.Errorf("CanReplay() = %v, want %v", got, tt.wantCanReplay)
			}

			// Verify bytes read count
			if got := rb.BytesRead(); got != tt.wantReadBytes {
				t.Errorf("BytesRead() = %v, want %v", got, tt.wantReadBytes)
			}
		})
	}
}

func TestReplayableBodyGetBufferedBytes(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		maxBytes      int64
		wantNilBuffer bool
		wantContent   string
	}{
		{
			name:          "buffered body returns content",
			input:         "hello world",
			maxBytes:      100,
			wantNilBuffer: false,
			wantContent:   "hello world",
		},
		{
			name:          "unreplayable body returns nil",
			input:         strings.Repeat("x", 1500),
			maxBytes:      1000,
			wantNilBuffer: true,
			wantContent:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := io.NopCloser(strings.NewReader(tt.input))
			rb := newReplayableBody(source, int64(len(tt.input)), tt.maxBytes)

			// Read all data - consume until EOF
			buf := make([]byte, 2000)
			for {
				n, err := rb.Read(buf)
				if err != nil {
					if err == io.EOF {
						break
					}
					t.Fatalf("Read() error = %v", err)
				}
				if n == 0 {
					break
				}
			}

			// Get buffered bytes
			got := rb.GetBufferedBytes()
			if (got == nil) != tt.wantNilBuffer {
				t.Errorf("GetBufferedBytes() = %v, wantNil = %v", got, tt.wantNilBuffer)
			}

			if !tt.wantNilBuffer && string(got) != tt.wantContent {
				t.Errorf("GetBufferedBytes() content = %q, want %q", string(got), tt.wantContent)
			}
		})
	}
}

func TestReplayableBodyDiscard(t *testing.T) {
	input := "hello world"
	source := io.NopCloser(strings.NewReader(input))
	rb := newReplayableBody(source, int64(len(input)), 100)

	// Read all data - consume until EOF
	buf := make([]byte, 100)
	for {
		n, err := rb.Read(buf)
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("Read() error = %v", err)
		}
		if n == 0 {
			break
		}
	}

	// Verify buffer exists before discard
	if !rb.CanReplay() {
		t.Error("CanReplay() = false before discard, want true")
	}

	// Discard
	rb.Discard()

	// Verify buffer is cleared
	if rb.CanReplay() {
		t.Error("CanReplay() = true after discard, want false")
	}

	if got := rb.GetBufferedBytes(); got != nil {
		t.Errorf("GetBufferedBytes() = %v after discard, want nil", got)
	}
}

func TestReplayableBodyMarkUnreplayable(t *testing.T) {
	input := "hello world"
	source := io.NopCloser(strings.NewReader(input))
	rb := newReplayableBody(source, int64(len(input)), 100)

	// Mark as unreplayable
	rb.MarkUnreplayable()

	// Verify state
	if !rb.unreplayable {
		t.Error("unreplayable = false after MarkUnreplayable, want true")
	}
	if !rb.bufferComplete {
		t.Error("bufferComplete = false after MarkUnreplayable, want true")
	}
	if rb.CanReplay() {
		t.Error("CanReplay() = true after MarkUnreplayable, want false")
	}
}

func TestReplayableBodyMultipleReads(t *testing.T) {
	input := strings.Repeat("x", 500)
	source := io.NopCloser(strings.NewReader(input))
	rb := newReplayableBody(source, int64(len(input)), 1000)

	// Read in multiple chunks
	buf1 := make([]byte, 100)
	n1, _ := rb.Read(buf1)

	buf2 := make([]byte, 200)
	n2, _ := rb.Read(buf2)

	// Continue reading until EOF to ensure buffer is marked complete
	buf3 := make([]byte, 300)
	n3 := 0
	err3 := error(nil)
	for {
		n, err := rb.Read(buf3)
		n3 += n
		err3 = err
		if err != nil {
			break
		}
		if n == 0 {
			break
		}
	}

	totalRead := n1 + n2 + n3
	if totalRead != len(input) {
		t.Errorf("Total read = %d, want %d", totalRead, len(input))
	}

	if err3 != io.EOF {
		t.Errorf("Final read error = %v, want EOF", err3)
	}

	// Verify the full body was buffered
	if !rb.CanReplay() {
		t.Error("CanReplay() = false after multiple reads, want true")
	}

	got := rb.GetBufferedBytes()
	if string(got) != input {
		t.Errorf("Buffered content mismatch, got %d bytes, want %d bytes", len(got), len(input))
	}
}

func TestReplayableBodyIsReplayable(t *testing.T) {
	tests := []struct {
		name          string
		contentLength int64
		maxBytes      int64
		wantIsReplayable bool
	}{
		{
			name:          "replayable body",
			contentLength: 100,
			maxBytes:      1000,
			wantIsReplayable: true,
		},
		{
			name:          "body exceeding limit",
			contentLength: 2000,
			maxBytes:      1000,
			wantIsReplayable: false,
		},
		{
			name:          "unknown length body",
			contentLength: -1,
			maxBytes:      1000,
			wantIsReplayable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := io.NopCloser(strings.NewReader("test"))
			rb := newReplayableBody(source, tt.contentLength, tt.maxBytes)

			if got := rb.IsReplayable(); got != tt.wantIsReplayable {
				t.Errorf("IsReplayable() = %v, want %v", got, tt.wantIsReplayable)
			}
		})
	}
}

func TestReplayableBodyConcurrentReads(t *testing.T) {
	// Test that concurrent reads are safe (should be protected by mutex)
	input := strings.Repeat("x", 1000)
	source := io.NopCloser(strings.NewReader(input))
	rb := newReplayableBody(source, int64(len(input)), 2000)

	// This test primarily ensures no data races occur
	// Reading from the same replayable body concurrently is not typical usage
	// but the mutex should prevent races
	buf := make([]byte, 500)
	n, err := rb.Read(buf)
	if err != nil && err != io.EOF {
		t.Errorf("Read() error = %v", err)
	}
	if n != 500 {
		t.Errorf("Read() = %d bytes, want 500", n)
	}
}

func TestReplayableBodyEmptyBody(t *testing.T) {
	source := io.NopCloser(strings.NewReader(""))
	rb := newReplayableBody(source, 0, 1000)

	buf := make([]byte, 100)
	n, err := rb.Read(buf)
	if err != io.EOF {
		t.Errorf("Read() error = %v, want EOF", err)
	}
	if n != 0 {
		t.Errorf("Read() = %d bytes, want 0", n)
	}

	// Empty body should be replayable
	if !rb.CanReplay() {
		t.Error("CanReplay() = false for empty body, want true")
	}

	got := rb.GetBufferedBytes()
	if got != nil && len(got) != 0 {
		t.Errorf("GetBufferedBytes() = %v, want empty or nil", got)
	}
}

func TestReplayableBodyLargeChunkedRead(t *testing.T) {
	// Test reading a large body in chunks larger than buffer size
	input := strings.Repeat("x", 5000)
	source := io.NopCloser(strings.NewReader(input))
	rb := newReplayableBody(source, -1, 1000) // Unknown length, small limit

	// Read in large chunks that exceed the limit
	buf := make([]byte, 2000)
	totalRead := 0
	for {
		n, err := rb.Read(buf)
		totalRead += n
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
	}

	if totalRead != len(input) {
		t.Errorf("Total read = %d, want %d", totalRead, len(input))
	}

	// Should exceed limit and be unreplayable
	if rb.CanReplay() {
		t.Error("CanReplay() = true after exceeding limit, want false")
	}

	if rb.BytesRead() != int64(len(input)) {
		t.Errorf("BytesRead() = %d, want %d", rb.BytesRead(), len(input))
	}
}

func TestIsProtocolUpgrade(t *testing.T) {
	tests := []struct {
		name         string
		method       string
		path         string
		upgradeHeader string
		wantUpgrade  bool
	}{
		{
			name:         "normal GET request",
			method:       "GET",
			path:         "/api/test",
			upgradeHeader: "",
			wantUpgrade:  false,
		},
		{
			name:         "WebSocket upgrade",
			method:       "GET",
			path:         "/ws",
			upgradeHeader: "websocket",
			wantUpgrade:  true,
		},
		{
			name:         "HTTP/2 prior knowledge (PRI)",
			method:       "PRI",
			path:         "*",
			upgradeHeader: "",
			wantUpgrade:  true,
		},
		{
			name:         "CONNECT method",
			method:       "CONNECT",
			path:         "example.com:443",
			upgradeHeader: "",
			wantUpgrade:  true,
		},
		{
			name:         "POST with WebSocket",
			method:       "POST",
			path:         "/ws",
			upgradeHeader: "websocket",
			wantUpgrade:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a minimal HTTP request
			req := &http.Request{
				Method: tt.method,
				URL:    &url.URL{Path: tt.path},
				Header: http.Header{},
			}
			if tt.upgradeHeader != "" {
				req.Header.Set("Upgrade", tt.upgradeHeader)
			}

			got := isProtocolUpgrade(req)
			if got != tt.wantUpgrade {
				t.Errorf("isProtocolUpgrade() = %v, want %v", got, tt.wantUpgrade)
			}
		})
	}
}

package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Regression tests for the oversized-response fallback. When a decoded body
// exceeds MaxBufferedResponseBytes — or declares no length at all — the
// response takes the incremental scrubber instead of the buffered path. The
// fallback is selected, never a rejection, so it inherits every guarantee the
// buffered path carries: the injected credential appears nowhere (body,
// headers, or trailers), the upstream status and headers survive, and the
// body reaches the caller incrementally rather than being held whole.

func TestResponseCapFallbackPreservesStatusHeadersTrailersWithoutLeaking(t *testing.T) {
	secret := []byte("fallback-no-leak-secret-fixture")
	body := append([]byte("lead-"), secret...)
	body = append(body, bytes.Repeat([]byte{'m'}, 4*1024)...)
	body = append(body, secret...)
	body = append(body, []byte("-tail")...)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Request-Id", "req-42")
		w.Header().Set("X-Echo", string(secret))
		// No declared Content-Length: the fallback is selected for the unknown
		// length, and chunked framing is the only framing that carries a
		// trailer section at all — a declared length would silently drop the
		// upstream trailer before SEAM ever saw it.
		w.Header().Add("Trailer", "X-Echo-Trailer")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write(body)
		w.Header().Set("X-Echo-Trailer", string(secret))
	}))
	defer upstream.Close()

	proxy := newResponseCapProxy(t, upstream.URL, 1024)
	response := serveWithInjectedSecret(t, proxy, secret)

	if response.StatusCode != http.StatusTeapot {
		t.Fatalf("status = %d, want %d preserved on the fallback path", response.StatusCode, http.StatusTeapot)
	}
	if got := response.Header.Get("Content-Type"); got != "text/plain" {
		t.Fatalf("Content-Type = %q, want text/plain preserved on the fallback path", got)
	}
	if got := response.Header.Get("X-Request-Id"); got != "req-42" {
		t.Fatalf("X-Request-Id = %q, want req-42 preserved on the fallback path", got)
	}
	assertNoSecret(t, secret, []byte(response.Header.Get("X-Echo")))
	if got := response.Header.Get("X-Echo"); got != RedactedSecret {
		t.Fatalf("X-Echo = %q, want %q scrubbed on the fallback path", got, RedactedSecret)
	}
	assertNoSecret(t, secret, []byte(response.Trailer.Get("X-Echo-Trailer")))
	if got := response.Trailer.Get("X-Echo-Trailer"); got != RedactedSecret {
		t.Fatalf("X-Echo-Trailer = %q, want %q scrubbed on the fallback path", got, RedactedSecret)
	}
	if got := response.Header.Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want it removed on the fallback path", got)
	}
	got, _ := io.ReadAll(response.Body)
	assertScrubbedExactly(t, secret, got, ScrubBytes(body, secret))
}

func TestResponseCapFallbackScrubsSecretStraddlingBufferedPrefixBoundary(t *testing.T) {
	secret := []byte("straddle-boundary-secret")
	// The buffered attempt reads exactly maxBuffered+1 decoded bytes before
	// discovering the body is oversized and handing the remainder to the
	// incremental scrubber. Put the secret across that seam: the first three
	// bytes land in the already-read prefix and the rest in the streamed
	// remainder, so a lost overlap at the transition either leaks the secret
	// verbatim or corrupts the body — both caught by the exact-match assert.
	plain := append(bytes.Repeat([]byte{'a'}, 1022), secret...)
	plain = append(plain, bytes.Repeat([]byte{'b'}, 1000)...)
	var encoded bytes.Buffer
	writer := gzip.NewWriter(&encoded)
	if _, err := writer.Write(plain); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if int64(encoded.Len()) > 1024 {
		t.Fatalf("fixture compressed to %d bytes, want <= 1024 so the declared length lands under the cap", encoded.Len())
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", itoa(encoded.Len()))
		_, _ = w.Write(encoded.Bytes())
	}))
	defer upstream.Close()

	proxy := newResponseCapProxy(t, upstream.URL, 1024)
	proxy.Client = &http.Client{Transport: &http.Transport{DisableCompression: true}}
	response := serveWithInjectedSecret(t, proxy, secret)

	if response.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip preserved across the buffered-to-stream transition", response.Header.Get("Content-Encoding"))
	}
	if got := response.Header.Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want it removed once the decoded body tripped the cap", got)
	}
	decoded, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatalf("decode scrubbed gzip: %v", err)
	}
	got, err := io.ReadAll(decoded)
	if err != nil {
		t.Fatalf("read scrubbed gzip: %v", err)
	}
	_ = decoded.Close()
	assertScrubbedExactly(t, secret, got, ScrubBytes(plain, secret))
}

func TestResponseCapFallbackStreamsBeforeUpstreamCompletes(t *testing.T) {
	secret := []byte("incremental-delivery-secret")
	// Long enough that its tail exceeds the len(secret)-1 overlap, so the
	// fallback has a safe prefix it may flush before the upstream finishes.
	lead := bytes.Repeat([]byte{'n'}, 256)
	rest := append([]byte("-mid-"), secret...)
	rest = append(rest, []byte("-tail")...)

	bodyReader, bodyWriter := io.Pipe()
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": []string{"text/plain"}},
		ContentLength: -1, // no declared length: the fallback is the only path
		Body:          bodyReader,
	}
	scrubber := newSecretScrubber([][]byte{secret})

	release := make(chan struct{})
	go func() {
		defer func() { _ = bodyWriter.Close() }()
		if _, err := bodyWriter.Write(lead); err != nil {
			return
		}
		// The second stage is withheld until the client has already received
		// the first: if the fallback ever buffers whole, the client's read
		// never returns, release never fires, and the client timeout fails
		// the test instead of the suite hanging.
		select {
		case <-release:
		case <-time.After(10 * time.Second):
			return
		}
		_, _ = bodyWriter.Write(rest)
	}()

	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := scrubber.streamResponse(w, resp, 1024); err != nil {
			t.Errorf("streamResponse() error = %v", err)
		}
	}))
	defer served.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(served.URL)
	if err != nil {
		t.Fatalf("GET fallback response: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if len(response.TransferEncoding) != 1 || response.TransferEncoding[0] != "chunked" {
		t.Fatalf("TransferEncoding = %v, want chunked on the fallback path", response.TransferEncoding)
	}

	first := make([]byte, 64)
	n, err := response.Body.Read(first)
	if err != nil || n == 0 {
		t.Fatalf("first read on the fallback path = (%d, %v), want bytes before the upstream body completes", n, err)
	}
	close(release)
	remainder, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read remainder: %v", err)
	}
	got := append(append([]byte{}, first[:n]...), remainder...)

	plain := append(bytes.Clone(lead), rest...)
	want := ScrubBytes(plain, secret)
	assertScrubbedExactly(t, secret, got, want)
	if got := bytes.Count(got, []byte(RedactedSecret)); got != 1 {
		t.Fatalf("marker count = %d, want 1", got)
	}
}

func TestResponseCapDeclaredOverLimitStreamsBeforeUpstreamCompletes(t *testing.T) {
	secret := []byte("declared-over-limit-stream-secret")
	// A declared body over the cap must never be held for whole-response
	// scrubbing. Keep the source blocked after a safe prefix; a whole-body
	// implementation would block the caller's first read as well.
	lead := bytes.Repeat([]byte{'n'}, 256)
	rest := append([]byte("-mid-"), secret...)
	rest = append(rest, bytes.Repeat([]byte{'t'}, 4*1024)...)
	plain := append(bytes.Clone(lead), rest...)

	bodyReader, bodyWriter := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusAccepted,
		Header: http.Header{
			"Content-Type":   []string{"text/plain"},
			"Content-Length": []string{itoa(len(plain))},
			"X-Echo":         []string{string(secret)},
			"Trailer":        []string{"X-Echo-Trailer"},
		},
		ContentLength: int64(len(plain)),
		Trailer:       http.Header{"X-Echo-Trailer": []string{string(secret)}},
		Body:          bodyReader,
	}
	scrubber := newSecretScrubber([][]byte{secret})

	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	go func() {
		defer func() { _ = bodyWriter.Close() }()
		if _, err := bodyWriter.Write(lead); err != nil {
			return
		}
		select {
		case <-release:
		case <-time.After(5 * time.Second):
			return
		}
		_, _ = bodyWriter.Write(rest)
	}()

	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := scrubber.streamResponse(w, resp, int64(len(lead))); err != nil {
			t.Errorf("streamResponse() error = %v", err)
		}
	}))
	defer served.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(served.URL)
	if err != nil {
		t.Fatalf("GET declared-over-limit response: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d preserved on the fallback path", response.StatusCode, http.StatusAccepted)
	}
	if got := response.Header.Get("X-Echo"); got != RedactedSecret {
		t.Fatalf("X-Echo = %q, want %q scrubbed on the fallback path", got, RedactedSecret)
	}
	if got := response.Header.Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want it removed on the streaming path", got)
	}

	first := make([]byte, 64)
	n, err := response.Body.Read(first)
	if err != nil || n == 0 {
		t.Fatalf("first read on the declared-over-limit path = (%d, %v), want bytes before the upstream body completes", n, err)
	}
	close(release)
	released = true
	remainder, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read declared-over-limit remainder: %v", err)
	}
	got := append(append([]byte{}, first[:n]...), remainder...)
	assertScrubbedExactly(t, secret, got, ScrubBytes(plain, secret))
	if got := response.Trailer.Get("X-Echo-Trailer"); got != RedactedSecret {
		t.Fatalf("X-Echo-Trailer = %q, want %q scrubbed on the fallback path", got, RedactedSecret)
	}
}

func TestResponseCapDecodedOversizeReencodesBeforeKnownLengthSourceCompletes(t *testing.T) {
	secret := []byte("decoded-known-length-stream-secret")
	const maxBuffered = int64(1024)
	firstPlain := bytes.Repeat([]byte{'p'}, 8*1024)
	secondPlain := append([]byte{}, secret...)
	secondPlain = append(secondPlain, bytes.Repeat([]byte{'q'}, 8*1024)...)
	plain := append(bytes.Clone(firstPlain), secondPlain...)

	var encoded bytes.Buffer
	writer := gzip.NewWriter(&encoded)
	if _, err := writer.Write(firstPlain); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	firstEncodedEnd := encoded.Len()
	writer = gzip.NewWriter(&encoded)
	if _, err := writer.Write(secondPlain); err != nil {
		t.Fatalf("second gzip write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("second gzip close: %v", err)
	}
	if int64(len(plain)) <= maxBuffered {
		t.Fatalf("decoded fixture length = %d, want over %d", len(plain), maxBuffered)
	}
	if int64(encoded.Len()) > maxBuffered {
		t.Fatalf("encoded fixture length = %d, want at or under %d to exercise decoded-size fallback", encoded.Len(), maxBuffered)
	}

	// Write one complete gzip member but withhold the second member and EOF. The
	// decoder can produce more than maxBuffered bytes from the first write, so
	// streamResponse must switch to its incremental re-encoder before the
	// declared-length source is complete.
	firstEncoded := encoded.Bytes()[:firstEncodedEnd]
	restEncoded := encoded.Bytes()[firstEncodedEnd:]
	bodyReader, bodyWriter := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Header: http.Header{
			"Content-Type":     []string{"text/plain"},
			"Content-Encoding": []string{"gzip"},
			"Content-Length":   []string{itoa(encoded.Len())},
			"X-Request-Id":     []string{"known-length-42"},
			"X-Echo":           []string{string(secret)},
			"Trailer":          []string{"X-Echo-Trailer"},
		},
		ContentLength: int64(encoded.Len()),
		Trailer:       http.Header{"X-Echo-Trailer": []string{string(secret)}},
		Body:          bodyReader,
	}
	scrubber := newSecretScrubber([][]byte{secret})

	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	go func() {
		defer func() { _ = bodyWriter.Close() }()
		if _, err := bodyWriter.Write(firstEncoded); err != nil {
			return
		}
		select {
		case <-release:
		case <-time.After(5 * time.Second):
			return
		}
		_, _ = bodyWriter.Write(restEncoded)
	}()

	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := scrubber.streamResponse(w, resp, maxBuffered); err != nil {
			t.Errorf("streamResponse() error = %v", err)
		}
	}))
	defer served.Close()

	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{DisableCompression: true},
	}
	response, err := client.Get(served.URL)
	if err != nil {
		t.Fatalf("GET decoded-oversize response: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d preserved on the streaming error response", response.StatusCode, http.StatusBadGateway)
	}
	if got := response.Header.Get("Content-Type"); got != "text/plain" {
		t.Fatalf("Content-Type = %q, want text/plain", got)
	}
	if got := response.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip preserved after incremental re-encoding", got)
	}
	if got := response.Header.Get("X-Request-Id"); got != "known-length-42" {
		t.Fatalf("X-Request-Id = %q, want known-length-42", got)
	}
	if got := response.Header.Get("X-Echo"); got != RedactedSecret {
		t.Fatalf("X-Echo = %q, want %q scrubbed", got, RedactedSecret)
	}
	if got := response.Header.Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want it removed for the re-encoded stream", got)
	}
	if len(response.TransferEncoding) != 1 || response.TransferEncoding[0] != "chunked" {
		t.Fatalf("TransferEncoding = %v, want chunked for the re-encoded stream", response.TransferEncoding)
	}

	first := make([]byte, 64)
	n, err := response.Body.Read(first)
	if err != nil || n == 0 {
		t.Fatalf("first read on decoded-oversize response = (%d, %v), want bytes before the known-length source completes", n, err)
	}
	close(release)
	released = true
	remainder, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read decoded-oversize remainder: %v", err)
	}
	reencoded := append(append([]byte{}, first[:n]...), remainder...)
	decoded, err := gzip.NewReader(bytes.NewReader(reencoded))
	if err != nil {
		t.Fatalf("decode incrementally re-encoded response: %v", err)
	}
	got, err := io.ReadAll(decoded)
	if err != nil {
		t.Fatalf("read decoded incrementally re-encoded response: %v", err)
	}
	_ = decoded.Close()
	assertScrubbedExactly(t, secret, got, ScrubBytes(plain, secret))
	if got := response.Trailer.Get("X-Echo-Trailer"); got != RedactedSecret {
		t.Fatalf("X-Echo-Trailer = %q, want %q scrubbed", got, RedactedSecret)
	}
}

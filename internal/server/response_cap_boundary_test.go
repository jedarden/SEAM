package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The boundary tests for MaxBufferedResponseBytes. The cap bounds only how much
// of a response may be held in memory for whole-body scrubbing: at or under the
// cap the response is scrubbed whole and keeps its Content-Length; over the cap
// — or without a declared length — the same scrubber runs incrementally with
// bounded memory and the response streams chunked. The cap never truncates and
// never rejects.

func newResponseCapProxy(t *testing.T, upstreamURL string, maxBuffered int64) *ReverseProxy {
	t.Helper()
	proxy, err := NewReverseProxyWithConfig(upstreamURL, &ReverseProxyConfig{MaxBufferedResponseBytes: maxBuffered})
	if err != nil {
		t.Fatalf("NewReverseProxyWithConfig() error = %v", err)
	}
	return proxy
}

func serveWithInjectedSecret(t *testing.T, proxy *ReverseProxy, secret []byte) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	withRouteMatch(req, &RouteMatch{Route: RouteEntry{
		PathTemplate: "/", Method: http.MethodGet, APIVersion: "v1",
		InjectAs: &InjectAs{Kind: InjectionHeader, Name: "X-Api-Key"},
	}}, func(context.Context, RouteEntry) ([]byte, error) { return secret, nil })
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, req)
	response := recorder.Result()
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func assertScrubbedExactly(t *testing.T, secret, body, want []byte) {
	t.Helper()
	assertNoSecret(t, secret, body)
	if !bytes.Equal(body, want) {
		t.Fatalf("scrubbed body = %q, want %q", body, want)
	}
	if !bytes.Contains(body, []byte(RedactedSecret)) {
		t.Fatalf("scrubbed body %q does not contain redaction marker", body)
	}
}

func TestResponseCapAtLimitBuffersWholeResponse(t *testing.T) {
	secret := []byte("at-cap-secret-fixture")
	body := append([]byte("head-"), secret...)
	body = append(body, []byte("-tail")...) // 26 bytes, Content-Length exactly at the 26-byte cap
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer upstream.Close()

	proxy := newResponseCapProxy(t, upstream.URL, int64(len(body)))
	response := serveWithInjectedSecret(t, proxy, secret)

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	got, _ := io.ReadAll(response.Body)
	want := ScrubBytes(body, secret)
	assertScrubbedExactly(t, secret, got, want)
	// The buffered path knows the scrubbed length and says so; a chunked
	// response here would mean the cap rejected an exactly-at-cap body.
	if got := response.Header.Get("Content-Length"); got != itoa(len(want)) {
		t.Fatalf("Content-Length = %q, want %q (buffered path preserves the length)", got, itoa(len(want)))
	}
}

func TestResponseCapOverLimitStreamsBoundedAndNeverTruncates(t *testing.T) {
	secret := []byte("over-cap-stream-secret-fixture")
	// 64 KiB against a 1 KiB cap: 64x the cap must still arrive complete.
	filler := bytes.Repeat([]byte{'x'}, 16*1024)
	body := append([]byte("lead-"), secret...)
	body = append(body, filler...)
	body = append(body, secret...)
	body = append(body, bytes.Repeat([]byte{'y'}, 16*1024)...)
	body = append(body, []byte("-trail-")...)
	body = append(body, secret...)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer upstream.Close()

	proxy := newResponseCapProxy(t, upstream.URL, 1024)
	response := serveWithInjectedSecret(t, proxy, secret)

	got, _ := io.ReadAll(response.Body)
	want := ScrubBytes(body, secret)
	assertScrubbedExactly(t, secret, got, want)
	// Over the cap the length is no longer known up front: the response is
	// chunked and Content-Length is gone.
	if got := response.Header.Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want it removed on the over-cap streaming path", got)
	}
	if bytes.Count(got, []byte(RedactedSecret)) != 3 {
		t.Fatalf("marker count = %d, want 3 (every occurrence scrubbed)", bytes.Count(got, []byte(RedactedSecret)))
	}
}

func TestResponseCapUnknownLengthStreamsIncrementally(t *testing.T) {
	secret := []byte("unknown-length-secret-fixture")
	chunks := [][]byte{
		append([]byte("chunk-one-"), secret...),
		append(append([]byte{}, secret...), []byte("-chunk-two")...),
		[]byte("chunk-three-clean"),
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, chunk := range chunks {
			_, _ = w.Write(chunk)
			w.(http.Flusher).Flush()
		}
	}))
	defer upstream.Close()

	proxy := newResponseCapProxy(t, upstream.URL, DefaultMaxBufferedResponseBytes)
	response := serveWithInjectedSecret(t, proxy, secret)

	got, _ := io.ReadAll(response.Body)
	var plain []byte
	for _, chunk := range chunks {
		plain = append(plain, chunk...)
	}
	want := ScrubBytes(plain, secret)
	assertScrubbedExactly(t, secret, got, want)
	if got := response.Header.Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want it removed on the unknown-length streaming path", got)
	}
}

func TestResponseCapDecodedExceedsDeclaredUnderCapFallsBackToStreaming(t *testing.T) {
	secret := []byte("decoded-over-cap-secret-fixture")
	// Compressed the body is far under the 1 KiB cap, so its Content-Length
	// claims the buffered path; decoded it is 64 KiB, so the cap must trip
	// mid-read and the already-read prefix must not be lost or duplicated.
	plain := append(bytes.Repeat([]byte("z"), 32*1024), secret...)
	plain = append(plain, bytes.Repeat([]byte{'q'}, 32*1024)...)
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
		t.Fatalf("Content-Encoding = %q, want gzip preserved on the fallback path", response.Header.Get("Content-Encoding"))
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
	want := ScrubBytes(plain, secret)
	assertScrubbedExactly(t, secret, got, want)
	if got := response.Header.Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want it removed once the decoded body tripped the cap", got)
	}
}

func TestResponseCapNonPositiveConfigFallsBackToDefault(t *testing.T) {
	secret := []byte("default-cap-secret-fixture")
	body := append([]byte("small-"), secret...)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer upstream.Close()

	// A non-positive cap is not "unbounded": the constructor and the scrubber
	// both normalize it to DefaultMaxBufferedResponseBytes.
	proxy := newResponseCapProxy(t, upstream.URL, 0)
	if proxy.MaxBufferedResponseBytes != DefaultMaxBufferedResponseBytes {
		t.Fatalf("proxy cap = %d, want the %d default", proxy.MaxBufferedResponseBytes, DefaultMaxBufferedResponseBytes)
	}
	response := serveWithInjectedSecret(t, proxy, secret)

	got, _ := io.ReadAll(response.Body)
	want := ScrubBytes(body, secret)
	assertScrubbedExactly(t, secret, got, want)
	if got := response.Header.Get("Content-Length"); got != itoa(len(want)) {
		t.Fatalf("Content-Length = %q, want %q (small body buffers under the default cap)", got, itoa(len(want)))
	}
}

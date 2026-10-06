package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

func TestResponseCapBelowLimitBuffersWholeResponse(t *testing.T) {
	secret := []byte("below-cap-secret-fixture")
	// 30 bytes against a 4 KiB cap: strictly below, with room to spare, so the
	// buffered path is the one that runs.
	body := append([]byte("head-"), secret...)
	body = append(body, []byte("-tail")...)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer upstream.Close()

	proxy := newResponseCapProxy(t, upstream.URL, 4096)
	response := serveWithInjectedSecret(t, proxy, secret)

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	got, _ := io.ReadAll(response.Body)
	want := ScrubBytes(body, secret)
	assertScrubbedExactly(t, secret, got, want)
	// Below the cap the response still takes the buffered path, so the
	// recomputed length survives; a chunked response here would mean the cap
	// demoted an under-cap body to streaming.
	if got := response.Header.Get("Content-Length"); got != itoa(len(want)) {
		t.Fatalf("Content-Length = %q, want %q (buffered path preserves the length below the cap too)", got, itoa(len(want)))
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

func TestResponseCapUnknownLengthCompressedBoundaryAndOverflow(t *testing.T) {
	const maxBuffered = int64(1024)
	secret := []byte("unknown-compressed-overflow-secret")

	for _, test := range []struct {
		name       string
		decodedLen int
	}{
		{name: "at decoded cap", decodedLen: int(maxBuffered)},
		{name: "over decoded cap", decodedLen: int(maxBuffered) + 512},
	} {
		t.Run(test.name, func(t *testing.T) {
			plain := bytes.Repeat([]byte{'x'}, test.decodedLen)
			secretOffset := test.decodedLen / 2
			copy(plain[secretOffset:], secret)
			var encoded bytes.Buffer
			writer := gzip.NewWriter(&encoded)
			if _, err := writer.Write(plain); err != nil {
				t.Fatalf("gzip write: %v", err)
			}
			// Keep the deflate stream open after flushing the complete decoded
			// body. This gives the reader data to deliver before the gzip footer
			// arrives, which is the compressed equivalent of an unknown-length
			// upstream that has not finished writing yet.
			if err := writer.Flush(); err != nil {
				t.Fatalf("gzip flush: %v", err)
			}
			streamPrefix := bytes.Clone(encoded.Bytes())
			if err := writer.Close(); err != nil {
				t.Fatalf("gzip close: %v", err)
			}
			streamSuffix := bytes.Clone(encoded.Bytes()[len(streamPrefix):])
			if len(streamPrefix) <= 10 || len(streamSuffix) == 0 {
				t.Fatalf("streaming gzip fixture did not produce a prefix and footer: prefix=%d suffix=%d", len(streamPrefix), len(streamSuffix))
			}

			bodyReader, bodyWriter := io.Pipe()
			resp := &http.Response{
				StatusCode: http.StatusBadGateway,
				Header: http.Header{
					"Content-Type":     []string{"text/plain"},
					"Content-Encoding": []string{"gzip"},
					"X-Request-Id":     []string{"unknown-compressed-42"},
					"X-Echo":           []string{string(secret)},
					"Trailer":          []string{"X-Echo-Trailer"},
				},
				ContentLength: -1,
				Trailer:       http.Header{"X-Echo-Trailer": []string{string(secret)}},
				Body:          bodyReader,
			}
			scrubber := newSecretScrubber([][]byte{secret})
			errCh := make(chan error, 1)

			release := make(chan struct{})
			released := false
			defer func() {
				if !released {
					close(release)
				}
			}()
			go func() {
				defer func() { _ = bodyWriter.Close() }()
				// gzip.NewReader initially asks for only its ten-byte header. Split
				// the pipe write there so the source can make progress without
				// requiring the decoder to consume the entire compressed body first.
				if _, err := bodyWriter.Write(streamPrefix[:10]); err != nil {
					return
				}
				if _, err := bodyWriter.Write(streamPrefix[10:]); err != nil {
					return
				}
				<-release
				_, _ = bodyWriter.Write(streamSuffix)
			}()

			served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				errCh <- scrubber.streamResponse(w, resp, maxBuffered)
			}))
			defer served.Close()

			client := &http.Client{
				Timeout:   5 * time.Second,
				Transport: &http.Transport{DisableCompression: true},
			}
			response, err := client.Get(served.URL)
			if err != nil {
				t.Fatalf("GET unknown-length compressed response: %v", err)
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusBadGateway {
				t.Fatalf("status = %d, want %d preserved on the compressed streaming path", response.StatusCode, http.StatusBadGateway)
			}
			if got := response.Header.Get("Content-Type"); got != "text/plain" {
				t.Fatalf("Content-Type = %q, want text/plain", got)
			}
			if got := response.Header.Get("Content-Encoding"); got != "gzip" {
				t.Fatalf("Content-Encoding = %q, want gzip preserved", got)
			}
			if got := response.Header.Get("X-Request-Id"); got != "unknown-compressed-42" {
				t.Fatalf("X-Request-Id = %q, want unknown-compressed-42", got)
			}
			if got := response.Header.Get("X-Echo"); got != RedactedSecret {
				t.Fatalf("X-Echo = %q, want %q scrubbed", got, RedactedSecret)
			}
			if got := response.Header.Get("Content-Length"); got != "" {
				t.Fatalf("Content-Length = %q, want it removed for unknown-length streaming", got)
			}
			if len(response.TransferEncoding) != 1 || response.TransferEncoding[0] != "chunked" {
				t.Fatalf("TransferEncoding = %v, want chunked for unknown-length streaming", response.TransferEncoding)
			}

			first := make([]byte, 64)
			n, err := response.Body.Read(first)
			if n == 0 {
				t.Fatalf("first read on unknown-length compressed response = (%d, %v), want bytes before source completion", n, err)
			}
			close(release)
			released = true
			remainder, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatalf("read unknown-length compressed remainder: %v", err)
			}
			encodedResponse := append(append([]byte{}, first[:n]...), remainder...)
			decoded, err := gzip.NewReader(bytes.NewReader(encodedResponse))
			if err != nil {
				t.Fatalf("decode scrubbed unknown-length response: %v", err)
			}
			got, err := io.ReadAll(decoded)
			if err != nil {
				t.Fatalf("read scrubbed unknown-length response: %v", err)
			}
			_ = decoded.Close()
			assertScrubbedExactly(t, secret, got, ScrubBytes(plain, secret))
			if got := response.Trailer.Get("X-Echo-Trailer"); got != RedactedSecret {
				t.Fatalf("X-Echo-Trailer = %q, want %q scrubbed", got, RedactedSecret)
			}
			if err := <-errCh; err != nil {
				assertNoSecret(t, secret, []byte(err.Error()))
				t.Fatalf("streamResponse() error = %v", err)
			}
		})
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

func TestResponseCapDeclaredOverLimitStillScrubsSmallBody(t *testing.T) {
	secret := []byte("declared-over-cap-secret")
	// The policy classifies by the declared length: a response claiming more
	// than the cap goes to the incremental scrubber even if the body turns out
	// six times smaller than it. Scrubbing must hold there too — the one way
	// this seam could leak is an oversized *declaration* routing a body to an
	// unscrubbed copy.
	body := append([]byte("small-"), secret...)
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": []string{"text/plain"}, "Content-Length": []string{"5000"}},
		ContentLength: 5000,
		Body:          io.NopCloser(bytes.NewReader(body)),
	}

	scrubber := newSecretScrubber([][]byte{secret})
	recorder := httptest.NewRecorder()
	if err := scrubber.streamResponse(recorder, resp, 1024); err != nil {
		t.Fatalf("streamResponse() error = %v", err)
	}
	response := recorder.Result()
	t.Cleanup(func() { _ = response.Body.Close() })

	got, _ := io.ReadAll(response.Body)
	want := ScrubBytes(body, secret)
	assertScrubbedExactly(t, secret, got, want)
	// The upstream's declared length was copied and then dropped: the streamed
	// body is the scrubbed 31 bytes, not the promised 5000.
	if got := response.Header.Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want it removed when the declared length exceeds the cap", got)
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

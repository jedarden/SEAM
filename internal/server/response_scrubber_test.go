package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"
	"go.yaml.in/yaml/v4"
)

type fixedChunkReader struct {
	data []byte
	size int
	off  int
}

func (r *fixedChunkReader) Read(p []byte) (int, error) {
	if r.off == len(r.data) {
		return 0, io.EOF
	}
	n := r.size
	if n > len(p) {
		n = len(p)
	}
	if remaining := len(r.data) - r.off; n > remaining {
		n = remaining
	}
	copy(p[:n], r.data[r.off:r.off+n])
	r.off += n
	return n, nil
}

func TestSecretScrubberEveryOffsetAndChunkSize(t *testing.T) {
	secrets := [][]byte{[]byte("short-fixture"), []byte("long-secret-fixture")}
	for offset := 0; offset <= len(secrets[1]); offset++ {
		t.Run("offset-"+itoa(offset), func(t *testing.T) {
			prefix := bytes.Repeat([]byte{'p'}, offset)
			body := append(append(append([]byte{}, prefix...), secrets[1]...), []byte("-suffix")...)
			want := ScrubBytes(body, secrets...)
			for chunkSize := 1; chunkSize <= len(body)+1; chunkSize++ {
				var got bytes.Buffer
				reader := &fixedChunkReader{data: body, size: chunkSize}
				if err := ScrubReader(&got, reader, secrets...); err != nil {
					t.Fatalf("chunk size %d: ScrubReader() error = %v", chunkSize, err)
				}
				if !bytes.Equal(got.Bytes(), want) {
					t.Fatalf("chunk size %d: got %q, want %q", chunkSize, got.Bytes(), want)
				}
				for _, secret := range secrets {
					if bytes.Contains(got.Bytes(), secret) {
						t.Fatalf("chunk size %d leaked %q in %q", chunkSize, secret, got.Bytes())
					}
				}
			}
		})
	}
}

func TestSecretScrubberUsesLongestMatch(t *testing.T) {
	body := []byte("before-credential-suffix")
	got := ScrubBytes(body, []byte("credential"), []byte("credential-suffix"))
	want := "before-" + RedactedSecret
	if string(got) != want {
		t.Fatalf("ScrubBytes() = %q, want %q", got, want)
	}
}

func FuzzSecretScrubber(f *testing.F) {
	f.Add([]byte("prefix-long-secret-fixture-suffix"), []byte("long-secret-fixture"), 1)
	f.Add([]byte("split-at-zero"), []byte("split-at-zero"), 2)
	f.Fuzz(func(t *testing.T, body, secret []byte, chunkSize int) {
		if len(secret) == 0 {
			t.Skip()
		}
		chunkSize = int(uint(chunkSize)%uint(len(body)+1)) + 1
		want := ScrubBytes(body, secret)
		var got bytes.Buffer
		if err := ScrubReader(&got, &fixedChunkReader{data: body, size: chunkSize}, secret); err != nil {
			t.Fatalf("ScrubReader() error = %v", err)
		}
		if !bytes.Equal(got.Bytes(), want) {
			t.Fatalf("chunk size %d: got %x, want %x", chunkSize, got.Bytes(), want)
		}
		// A one-byte secret that is itself part of the mandated marker cannot
		// be absent from the marker; equality with the contiguous scrub result
		// remains the invariant for that degenerate fixture.
		if !bytes.Contains([]byte(RedactedSecret), secret) && bytes.Contains(got.Bytes(), secret) {
			t.Fatalf("scrubbed output contains secret at chunk size %d", chunkSize)
		}
	})
}

func TestInjectedResponseScrubsBodyHeadersAndTrailers(t *testing.T) {
	secret := []byte("header-body-trailer-fixture")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Echo", string(secret))
		w.Header().Add("Trailer", "X-Echo-Trailer")
		body := append([]byte("body-before-"), secret...)
		body = append(body, []byte("-body-after")...)
		w.Header().Set("Content-Length", itoa(len(body)))
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write(body)
		w.Header().Set("X-Echo-Trailer", string(secret))
	}))
	defer upstream.Close()

	proxy, err := NewReverseProxyWithConfig(upstream.URL, &ReverseProxyConfig{MaxBufferedResponseBytes: 1024})
	if err != nil {
		t.Fatalf("NewReverseProxyWithConfig() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	withRouteMatch(req, &RouteMatch{Route: RouteEntry{
		PathTemplate: "/", Method: http.MethodGet, APIVersion: "v1",
		InjectAs: &InjectAs{Kind: InjectionHeader, Name: "X-Api-Key"},
	}}, func(context.Context, RouteEntry) ([]byte, error) {
		return secret, nil
	})
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, req)
	response := recorder.Result()
	defer response.Body.Close()

	if response.StatusCode != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusTeapot)
	}
	body, _ := io.ReadAll(response.Body)
	assertNoSecret(t, secret, body)
	assertNoSecret(t, secret, []byte(response.Header.Get("X-Echo")))
	assertNoSecret(t, secret, []byte(response.Trailer.Get("X-Echo-Trailer")))
	if !bytes.Contains(body, []byte(RedactedSecret)) {
		t.Fatalf("body %q does not contain redaction marker", body)
	}
}

func TestInjectedGzipResponseIsDecodedScrubbedAndReencoded(t *testing.T) {
	secret := []byte("gzip-secret-fixture")
	plain := append([]byte("gzip-before-"), secret...)
	plain = append(plain, []byte("-gzip-after")...)
	var encoded bytes.Buffer
	writer := gzip.NewWriter(&encoded)
	_, _ = writer.Write(plain)
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", itoa(encoded.Len()))
		_, _ = w.Write(encoded.Bytes())
	}))
	defer upstream.Close()
	proxy, err := NewReverseProxyWithConfig(upstream.URL, &ReverseProxyConfig{MaxBufferedResponseBytes: 1024})
	if err != nil {
		t.Fatalf("NewReverseProxyWithConfig() error = %v", err)
	}
	proxy.Client = &http.Client{Transport: &http.Transport{DisableCompression: true}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	withRouteMatch(req, &RouteMatch{Route: RouteEntry{
		PathTemplate: "/", Method: http.MethodGet, APIVersion: "v1",
		InjectAs: &InjectAs{Kind: InjectionHeader, Name: "X-Api-Key"},
	}}, func(context.Context, RouteEntry) ([]byte, error) { return secret, nil })
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, req)
	response := recorder.Result()
	defer response.Body.Close()
	if response.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", response.Header.Get("Content-Encoding"))
	}
	decoded, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatalf("decode scrubbed gzip: %v", err)
	}
	body, err := io.ReadAll(decoded)
	if err != nil {
		t.Fatalf("read scrubbed gzip: %v", err)
	}
	_ = decoded.Close()
	assertNoSecret(t, secret, body)
	if !bytes.Contains(body, []byte(RedactedSecret)) {
		t.Fatalf("decoded body %q does not contain redaction marker", body)
	}
}

func TestInjectedOversizedResponseUsesIncrementalScrubbing(t *testing.T) {
	secret := []byte("oversized-stream-secret-fixture")
	body := append(bytes.Repeat([]byte("x"), 64), secret...)
	body = append(body, bytes.Repeat([]byte("y"), 64)...)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer upstream.Close()
	proxy, err := NewReverseProxyWithConfig(upstream.URL, &ReverseProxyConfig{MaxBufferedResponseBytes: 16})
	if err != nil {
		t.Fatalf("NewReverseProxyWithConfig() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	withRouteMatch(req, &RouteMatch{Route: RouteEntry{
		PathTemplate: "/", Method: http.MethodGet, APIVersion: "v1",
		InjectAs: &InjectAs{Kind: InjectionHeader, Name: "X-Api-Key"},
	}}, func(context.Context, RouteEntry) ([]byte, error) { return secret, nil })
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, req)
	response := recorder.Result()
	defer response.Body.Close()
	got, _ := io.ReadAll(response.Body)
	assertNoSecret(t, secret, got)
	if !bytes.Contains(got, []byte(RedactedSecret)) {
		t.Fatalf("incrementally scrubbed body %q does not contain redaction marker", got)
	}
}

func TestInjectedOpaqueResponseRefusesWithoutAcknowledgement(t *testing.T) {
	secret := []byte("opaque-secret-fixture")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(append([]byte("opaque-"), secret...))
	}))
	defer upstream.Close()
	proxy, err := NewReverseProxy(upstream.URL)
	if err != nil {
		t.Fatalf("NewReverseProxy() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	withRouteMatch(req, &RouteMatch{Route: RouteEntry{
		PathTemplate: "/", Method: http.MethodGet, APIVersion: "v1",
		InjectAs: &InjectAs{Kind: InjectionHeader, Name: "X-Api-Key"},
	}}, func(context.Context, RouteEntry) ([]byte, error) { return secret, nil })
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	assertNoSecret(t, secret, recorder.Body.Bytes())
}

func TestInjectedAcknowledgedOpaqueResponsePassesThrough(t *testing.T) {
	secret := []byte("acknowledged-opaque-secret-fixture")
	expected := append([]byte("opaque-"), secret...)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(expected)
	}))
	defer upstream.Close()
	proxy, err := NewReverseProxy(upstream.URL)
	if err != nil {
		t.Fatalf("NewReverseProxy() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	withRouteMatch(req, &RouteMatch{Route: RouteEntry{
		PathTemplate: "/", Method: http.MethodGet, APIVersion: "v1", Unscrubbable: true,
		InjectAs: &InjectAs{Kind: InjectionHeader, Name: "X-Api-Key"},
	}}, func(context.Context, RouteEntry) ([]byte, error) { return secret, nil })
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !bytes.Equal(recorder.Body.Bytes(), expected) {
		t.Fatalf("acknowledged opaque body = %q, want %q", recorder.Body.Bytes(), expected)
	}
}

func TestInjectedUnsupportedEncodingRefusesWithoutAcknowledgement(t *testing.T) {
	secret := []byte("unsupported-encoding-secret-fixture")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Encoding", "br")
		_, _ = w.Write(append([]byte("encoded-"), secret...))
	}))
	defer upstream.Close()
	proxy, err := NewReverseProxy(upstream.URL)
	if err != nil {
		t.Fatalf("NewReverseProxy() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	withRouteMatch(req, &RouteMatch{Route: RouteEntry{
		PathTemplate: "/", Method: http.MethodGet, APIVersion: "v1",
		InjectAs: &InjectAs{Kind: InjectionHeader, Name: "X-Api-Key"},
	}}, func(context.Context, RouteEntry) ([]byte, error) { return secret, nil })
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	assertNoSecret(t, secret, recorder.Body.Bytes())
}

func TestInjectedProtocolUpgradeRefusesWithoutAcknowledgement(t *testing.T) {
	secret := []byte("protocol-upgrade-secret-fixture")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("upstream received a protocol-upgrade request despite refused injection")
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))
	defer upstream.Close()
	proxy, err := NewReverseProxy(upstream.URL)
	if err != nil {
		t.Fatalf("NewReverseProxy() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	withRouteMatch(req, &RouteMatch{Route: RouteEntry{
		PathTemplate: "/", Method: http.MethodGet, APIVersion: "v1",
		InjectAs: &InjectAs{Kind: InjectionHeader, Name: "X-Api-Key"},
	}}, func(context.Context, RouteEntry) ([]byte, error) { return secret, nil })
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	assertNoSecret(t, secret, recorder.Body.Bytes())
}

func TestInjectedCredentialDoesNotReachLogSink(t *testing.T) {
	secret := []byte("log-capture-secret-fixture")
	var logs bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	}()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This is the sink under test: the upstream receives the credential, but
		// neither the proxy's audit logs nor caller-visible output may contain it.
		if r.Header.Get("X-Api-Key") != string(secret) {
			t.Errorf("upstream did not receive injected credential")
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write(append([]byte("echo-"), secret...))
	}))
	defer upstream.Close()
	proxy, err := NewReverseProxy(upstream.URL)
	if err != nil {
		t.Fatalf("NewReverseProxy() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	withRouteMatch(req, &RouteMatch{Route: RouteEntry{
		PathTemplate: "/", Method: http.MethodGet, APIVersion: "v1",
		InjectAs: &InjectAs{Kind: InjectionHeader, Name: "X-Api-Key"},
	}}, func(context.Context, RouteEntry) ([]byte, error) { return secret, nil })
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, req)
	assertNoSecret(t, secret, logs.Bytes())
	assertNoSecret(t, secret, recorder.Body.Bytes())
}

func TestRouteTableCarriesUnscrubbableAcknowledgement(t *testing.T) {
	document := &v3.Document{Paths: &v3.Paths{PathItems: orderedmap.New[string, *v3.PathItem]()}}
	operation := &v3.Operation{Responses: &v3.Responses{Codes: orderedmap.New[string, *v3.Response]()}}
	operation.Responses.Codes.Set("200", &v3.Response{Description: "OK"})
	operation.Extensions = orderedmap.New[string, *yaml.Node]()
	operation.Extensions.Set("x-upstream", &yaml.Node{Kind: yaml.ScalarNode, Value: "http://upstream.test"})
	operation.Extensions.Set("x-unscrubbable", &yaml.Node{Kind: yaml.ScalarNode, Value: "acknowledged"})
	document.Paths.PathItems.Set("/opaque", &v3.PathItem{Get: operation})

	table, err := BuildRouteTable(document)
	if err != nil {
		t.Fatalf("BuildRouteTable() error = %v", err)
	}
	routes := table.GetRoutes()
	if len(routes) != 1 || !routes[0].Unscrubbable {
		t.Fatalf("route acknowledgement was not carried: %+v", routes)
	}
}

func assertNoSecret(t *testing.T, secret, value []byte) {
	t.Helper()
	if bytes.Contains(value, secret) {
		t.Fatalf("value leaked injected credential")
	}
}

func itoa(value int) string {
	return strconv.Itoa(value)
}

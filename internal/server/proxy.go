package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ardenone/seam/internal/vault"
)

const (
	upstreamConnectTimeout  = 5 * time.Second
	upstreamRequestTimeout  = 30 * time.Second
	upstreamMaxIdleConns    = 100
	upstreamIdleConnTimeout = 90 * time.Second

	// DefaultUpstreamCADir is the default mount point for the upstream CA ConfigMap.
	// Local development can override this with --upstream-ca-dir.
	// This is the ConfigMap mount point in production deployments.
	DefaultUpstreamCADir = "/etc/gateway/upstream-ca"

	// DefaultUpstreamAllowlistFile is the fixed mount point for the upstream
	// allowlist ConfigMap in production deployments. Local development can
	// override it with --allowlist-file.
	DefaultUpstreamAllowlistFile = "/etc/gateway/allowlist.yaml"
)

// Context key type for storing replayable body in request context
type contextKey int

const (
	replayableBodyKey contextKey = iota
)

// contextWithReplayableBody stores a replayable body in the context
func contextWithReplayableBody(ctx context.Context, rb *replayableBody) context.Context {
	return context.WithValue(ctx, replayableBodyKey, rb)
}

// replayableBodyFromContext extracts the replayable body from the context.
// Read side of the Phase 2.5 replay-body context key; contextWithReplayableBody
// already stores it, but no caller reads it back yet.
//
//nolint:unused
func replayableBodyFromContext(ctx context.Context) *replayableBody {
	if rb, ok := ctx.Value(replayableBodyKey).(*replayableBody); ok {
		return rb
	}
	return nil
}

// isProtocolUpgrade detects if the request is a protocol upgrade (WebSocket, HTTP/2, etc.)
// Protocol upgrades are unreplayable because the connection semantics change mid-stream.
func isProtocolUpgrade(r *http.Request) bool {
	if r == nil {
		return false
	}

	// Check for Upgrade header (WebSocket, etc.)
	if r.Header.Get("Upgrade") != "" {
		return true
	}

	// Check for HTTP/2 prior knowledge (PRI request method)
	if r.Method == "PRI" && r.URL.Path == "*" {
		return true
	}

	// Check for CONNECT method (tunneling)
	if r.Method == "CONNECT" {
		return true
	}

	return false
}

// defaultUpstreamClient is shared by standalone ForwardRequest calls. Sharing
// the client also shares its Transport, so requests to the same upstream can
// reuse pooled connections.
var defaultUpstreamClient = newUpstreamHTTPClient()

// buildTLSConfig creates a tls.Config from the route's UpstreamTLSConfig.
// Absent or nil tlsConfig means system trust store with hostname checking (default secure behavior).
func buildTLSConfig(tlsConfig *UpstreamTLSConfig, upstreamCADir string) (*tls.Config, error) {
	if tlsConfig == nil {
		// Default: system trust store with hostname checking
		return &tls.Config{
			MinVersion: tls.VersionTLS12,
		}, nil
	}

	config := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	// Load custom CA bundle if specified
	if tlsConfig.CaBundle != "" {
		caPath := filepath.Join(upstreamCADir, tlsConfig.CaBundle)
		caPEM, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA bundle %s: %w", caPath, err)
		}

		certPool := x509.NewCertPool()
		if !certPool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("failed to parse CA bundle from %s", caPath)
		}
		config.RootCAs = certPool
	}

	// Set ServerName for SNI override if specified
	if tlsConfig.ServerName != "" {
		config.ServerName = tlsConfig.ServerName
	}

	// insecureSkipVerify is ONLY allowed when explicitly set to "acknowledged"
	// in the fragment. This is never set globally.
	config.InsecureSkipVerify = tlsConfig.InsecureSkipVerify

	return config, nil
}

// newUpstreamHTTPClient creates the client used for outbound upstream calls.
// The dial and TLS handshake limits bound connection establishment while the
// client timeout bounds the complete request, including waiting for a response.
func newUpstreamHTTPClient() *http.Client {
	client, err := newUpstreamHTTPClientWithTLS(nil, DefaultUpstreamCADir)
	if err != nil {
		// The default configuration does not read any files, so this should be
		// unreachable. Keep package initialization fail-fast if that invariant
		// ever changes.
		panic(fmt.Sprintf("create default upstream HTTP client: %v", err))
	}
	return client
}

// newUpstreamHTTPClientWithTLS creates a client with optional TLS configuration.
// If tlsConfig is nil, it uses the system trust store with hostname checking.
// It returns an error when the requested TLS configuration cannot be built.
func newUpstreamHTTPClientWithTLS(tlsConfig *UpstreamTLSConfig, upstreamCADir string) (*http.Client, error) {
	// Build the TLS configuration before constructing the transport. A route
	// configuration error must be returned to the caller; silently replacing it
	// with the system trust store could send credentials to an unintended host.
	tlsConf, err := buildTLSConfig(tlsConfig, upstreamCADir)
	if err != nil {
		return nil, fmt.Errorf("build upstream TLS config: %w", err)
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   upstreamConnectTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          upstreamMaxIdleConns,
		MaxIdleConnsPerHost:   upstreamMaxIdleConns,
		IdleConnTimeout:       upstreamIdleConnTimeout,
		TLSHandshakeTimeout:   upstreamConnectTimeout,
		ResponseHeaderTimeout: upstreamRequestTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       tlsConf,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   upstreamRequestTimeout,
	}, nil
}

// upstreamTLSConfigKey returns a stable identity for the effective per-route
// TLS settings. It is used to share one connection-pooled client for each
// distinct TLS configuration.
func upstreamTLSConfigKey(tlsConfig *UpstreamTLSConfig) string {
	if tlsConfig == nil {
		return "tls:default"
	}
	return fmt.Sprintf("tls:%q:%q:%t:%t", tlsConfig.CaBundle, tlsConfig.ServerName, tlsConfig.InsecureSkipVerify, tlsConfig.PlaintextAck)
}

func upstreamProxyCacheKey(upstreamURL string, tlsConfig *UpstreamTLSConfig) string {
	return fmt.Sprintf("upstream:%q:%s", upstreamURL, upstreamTLSConfigKey(tlsConfig))
}

// ReverseProxy is an HTTP reverse proxy that forwards requests to upstream
// services and streams their responses back to the caller.
type ReverseProxy struct {
	Client *http.Client

	// UpstreamURL is the base URL of the upstream service.
	UpstreamURL *url.URL

	// UpstreamHost is the Host value sent to the upstream service.
	UpstreamHost string

	// RequestTimeout bounds the outbound request when this proxy is used as an
	// http.Handler. ForwardRequest uses upstreamRequestTimeout.
	RequestTimeout time.Duration

	BufferPool *bufferPool

	// MaxReplayableRequestBytes is the maximum request body size to buffer for replay.
	// This is an independent knob from response buffering. Default: 1 MiB.
	MaxReplayableRequestBytes int64

	// MaxBufferedResponseBytes is the maximum decoded response body held for
	// whole-body scrubbing. Larger and unknown-length bodies use incremental
	// scrubbing instead. Default: 1 MiB.
	MaxBufferedResponseBytes int64
}

// NewReverseProxy creates a reverse proxy for upstreamURL.
func NewReverseProxy(upstreamURL string) (*ReverseProxy, error) {
	return NewReverseProxyWithConfig(upstreamURL, nil)
}

// ReverseProxyConfig holds configuration for creating a ReverseProxy.
type ReverseProxyConfig struct {
	MaxReplayableRequestBytes int64
	MaxBufferedResponseBytes  int64
	// TLSConfig selects the route-specific TLS settings for the upstream
	// client. It is ignored when Client is supplied.
	TLSConfig *UpstreamTLSConfig
	// UpstreamCADir is the directory containing named CA bundles.
	UpstreamCADir string
	// Client can be supplied by a server-level TLS client cache.
	Client *http.Client
}

// NewReverseProxyWithConfig creates a reverse proxy with custom configuration.
func NewReverseProxyWithConfig(upstreamURL string, cfg *ReverseProxyConfig) (*ReverseProxy, error) {
	parsedURL, err := parseUpstreamBaseURL(upstreamURL)
	if err != nil {
		return nil, err
	}

	maxReplayable := int64(DefaultMaxReplayableRequestBytes)
	if cfg != nil && cfg.MaxReplayableRequestBytes > 0 {
		maxReplayable = cfg.MaxReplayableRequestBytes
	}
	maxBufferedResponse := DefaultMaxBufferedResponseBytes
	if cfg != nil && cfg.MaxBufferedResponseBytes > 0 {
		maxBufferedResponse = cfg.MaxBufferedResponseBytes
	}

	client := defaultUpstreamClient
	if cfg != nil && cfg.Client != nil {
		client = cfg.Client
	} else if cfg != nil && cfg.TLSConfig != nil {
		upstreamCADir := cfg.UpstreamCADir
		if upstreamCADir == "" {
			upstreamCADir = DefaultUpstreamCADir
		}
		client, err = newUpstreamHTTPClientWithTLS(cfg.TLSConfig, upstreamCADir)
		if err != nil {
			return nil, fmt.Errorf("create upstream HTTP client: %w", err)
		}
	}

	return &ReverseProxy{
		Client:                    client,
		UpstreamURL:               parsedURL,
		UpstreamHost:              parsedURL.Host,
		RequestTimeout:            upstreamRequestTimeout,
		BufferPool:                newBufferPool(),
		MaxReplayableRequestBytes: maxReplayable,
		MaxBufferedResponseBytes:  maxBufferedResponse,
	}, nil
}

// ServeHTTP implements http.Handler for the reverse proxy.
func (p *ReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	trackedWriter := &responseWriterTracker{ResponseWriter: w}
	ctx := r.Context()
	match := routeMatchFromRequest(r)
	if match != nil && match.Route.InjectAs != nil {
		if isProtocolUpgrade(r) && !match.Route.Unscrubbable {
			p.handleError(trackedWriter, r, errUnscannableResponse, "refusing credential injection into protocol upgrade")
			return
		}
		resolver := routeSecretResolverFromRequest(r)
		if resolver == nil {
			p.handleError(trackedWriter, r, fmt.Errorf("credential resolver is not configured"), "injecting upstream credential")
			return
		}
		secret, err := resolver(ctx, match.Route)
		if err != nil {
			p.handleError(trackedWriter, r, err, "resolving upstream credential")
			return
		}
		if err := InjectSecret(r, match.Route.InjectAs, secret); err != nil {
			p.handleError(trackedWriter, r, err, "injecting upstream credential")
			return
		}
		ctx = withResponseScrub(ctx, secret, match.Route.Unscrubbable)
	}

	outgoingURL, err := p.buildUpstreamURLForMatch(r, match)
	if err != nil {
		p.handleError(trackedWriter, r, err, "building upstream path")
		return
	}
	upstreamReq, err := p.buildUpstreamRequest(ctx, r, outgoingURL)
	if err != nil {
		p.handleError(trackedWriter, r, err, "building upstream request")
		return
	}

	if err := p.dispatchAndServe(ctx, trackedWriter, r, upstreamReq); err != nil {
		p.handleError(trackedWriter, r, err, "upstream request")
	}
}

func (p *ReverseProxy) buildUpstreamURLForMatch(r *http.Request, match *RouteMatch) (string, error) {
	if p == nil || p.UpstreamURL == nil || r == nil || r.URL == nil {
		return "", nil
	}
	if match != nil {
		path, err := ComputeUpstreamPath(match)
		if err != nil {
			return "", err
		}
		return buildUpstreamURLWithEscapedPath(p.UpstreamURL, path, r.URL.RawQuery), nil
	}
	return buildUpstreamURL(p.UpstreamURL, r.URL.Path, r.URL.RawQuery), nil
}

// buildUpstreamRequest creates the request sent to the upstream service.
// Phase 2.5: Integrates replayable body tee to buffer request body up to
// MaxReplayableRequestBytes while streaming upstream.
func (p *ReverseProxy) buildUpstreamRequest(ctx context.Context, inboundReq *http.Request, outgoingURL string) (*http.Request, error) {
	if inboundReq == nil {
		return nil, fmt.Errorf("incoming request is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Phase 2.5: Wrap request body with replayable tee if present
	var requestBody = inboundReq.Body
	if inboundReq.Body != nil {
		// Detect unreplayable conditions (protocol upgrades)
		if isProtocolUpgrade(inboundReq) {
			// Protocol upgrade (WebSocket, HTTP/2 prior knowledge, etc.)
			// Stream through unbuffered - mark as unreplayable
			log.Printf("[proxy] Protocol upgrade detected - body not buffered for replay: %s", inboundReq.Header.Get("Upgrade"))
		} else if inboundReq.Body == http.NoBody {
			// No body present - use as-is
			requestBody = inboundReq.Body
		} else {
			// Wrap body with replayable tee
			// ContentLength of -1 means unknown (chunked or no Content-Length header)
			replayable := newReplayableBody(inboundReq.Body, inboundReq.ContentLength, p.MaxReplayableRequestBytes)
			requestBody = replayable

			// Store replayable body in request context for later retrieval by Phase 12/13
			// This allows the 401 retry logic to access the buffered body without re-reading
			ctx = contextWithReplayableBody(ctx, replayable)
		}
	}

	outboundReq, err := http.NewRequestWithContext(ctx, inboundReq.Method, outgoingURL, requestBody)
	if err != nil {
		return nil, fmt.Errorf("creating outbound request: %w", err)
	}
	copyRequestMetadata(inboundReq, outboundReq)
	return outboundReq, nil
}

// dispatchAndServe sends the request to the upstream and streams its response
// without buffering the body in memory.
func (p *ReverseProxy) dispatchAndServe(ctx context.Context, w http.ResponseWriter, _ *http.Request, upstreamReq *http.Request) error {
	if upstreamReq == nil {
		return fmt.Errorf("upstream request is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	timeout := p.RequestTimeout
	if timeout <= 0 {
		timeout = upstreamRequestTimeout
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	upstreamReq = upstreamReq.WithContext(timeoutCtx)

	client := p.Client
	if client == nil {
		client = defaultUpstreamClient
	}
	startTime := time.Now()
	upstreamResp, err := client.Do(upstreamReq)
	if err != nil {
		cancel()
		return fmt.Errorf("upstream request failed: %w", err)
	}
	upstreamResp.Body = &cancelOnClose{ReadCloser: upstreamResp.Body, cancel: cancel}
	defer func() { _ = upstreamResp.Body.Close() }()

	log.Printf("[proxy] Upstream %s %s -> %d (%v)", upstreamReq.Method, upstreamReq.URL.Path, upstreamResp.StatusCode, time.Since(startTime))

	if scrubConfig := responseScrubFromContext(upstreamReq.Context()); scrubConfig != nil && len(scrubConfig.secrets) > 0 {
		scrubber := newSecretScrubber(scrubConfig.secrets)
		_, encodingsErr := parseContentEncodings(upstreamResp.Header.Get("Content-Encoding"))
		if responseIsOpaque(upstreamResp) || encodingsErr != nil {
			if !scrubConfig.allowUnscannable {
				return errUnscannableResponse
			}
			if err := scrubber.serveUnscannable(w, upstreamResp); err != nil {
				return fmt.Errorf("streaming acknowledged unscannable response: %w", err)
			}
			return nil
		}
		if err := scrubber.streamResponse(w, upstreamResp, p.MaxBufferedResponseBytes); err != nil {
			return fmt.Errorf("scrubbing upstream response: %w", err)
		}
		return nil
	}

	copyHeaders(upstreamResp.Header, w.Header())
	w.WriteHeader(upstreamResp.StatusCode)
	if err := p.streamResponse(w, upstreamResp.Body); err != nil {
		return fmt.Errorf("streaming upstream response: %w", err)
	}
	return nil
}

// copyHeaders copies end-to-end headers while removing standard hop-by-hop
// headers and headers named by the incoming Connection header.
func copyHeaders(inbound, outbound http.Header) {
	hopByHop := hopByHopHeaders(inbound)
	for key, values := range inbound {
		canonicalKey := http.CanonicalHeaderKey(key)
		if _, skip := hopByHop[canonicalKey]; skip {
			continue
		}
		for _, value := range values {
			outbound.Add(canonicalKey, value)
		}
	}
}

// copyRequestMetadata copies the request headers and special Host value.
func copyRequestMetadata(inbound, outbound *http.Request) {
	copyHeaders(inbound.Header, outbound.Header)
	if inbound.ContentLength >= 0 {
		outbound.ContentLength = inbound.ContentLength
	}
	outbound.GetBody = inbound.GetBody
	outbound.Host = outbound.URL.Host
	setForwardedHeaders(inbound, outbound)
}

func hopByHopHeaders(headers http.Header) map[string]struct{} {
	hopByHop := map[string]struct{}{
		http.CanonicalHeaderKey("Connection"):          {},
		http.CanonicalHeaderKey("Keep-Alive"):          {},
		http.CanonicalHeaderKey("Proxy-Authenticate"):  {},
		http.CanonicalHeaderKey("Proxy-Authorization"): {},
		http.CanonicalHeaderKey("Proxy-Connection"):    {},
		http.CanonicalHeaderKey("TE"):                  {},
		http.CanonicalHeaderKey("Trailer"):             {},
		http.CanonicalHeaderKey("Transfer-Encoding"):   {},
		http.CanonicalHeaderKey("Upgrade"):             {},
	}

	for _, connectionValue := range headers.Values("Connection") {
		for _, token := range strings.Split(connectionValue, ",") {
			token = strings.TrimSpace(token)
			if token != "" {
				hopByHop[http.CanonicalHeaderKey(token)] = struct{}{}
			}
		}
	}
	return hopByHop
}

// setForwardedHeaders adds forwarding metadata for upstream logging.
func setForwardedHeaders(inboundReq, outboundReq *http.Request) {
	clientIP := remoteClientIP(inboundReq)
	existingXFF := strings.TrimSpace(inboundReq.Header.Get("X-Forwarded-For"))
	if clientIP == "" {
		clientIP = getClientIP(inboundReq)
	}
	switch {
	case existingXFF != "" && clientIP != "":
		outboundReq.Header.Set("X-Forwarded-For", existingXFF+", "+clientIP)
	case existingXFF != "":
		outboundReq.Header.Set("X-Forwarded-For", existingXFF)
	case clientIP != "":
		outboundReq.Header.Set("X-Forwarded-For", clientIP)
	}

	proto := "http"
	if inboundReq.URL != nil && inboundReq.URL.Scheme != "" {
		proto = inboundReq.URL.Scheme
	} else if inboundReq.TLS != nil {
		proto = "https"
	}
	outboundReq.Header.Set("X-Forwarded-Proto", proto)

	if inboundReq.Host != "" {
		outboundReq.Header.Set("X-Forwarded-Host", inboundReq.Host)
	}
}

// getClientIP returns the original client address using the first available
// forwarding header, then the request's remote address.
func getClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	return remoteClientIP(r)
}

func remoteClientIP(r *http.Request) string {
	if r == nil || r.RemoteAddr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// ForwardRequest forwards an incoming request to upstreamBase and returns the
// live upstream response. The response body is left open for the caller to
// stream and must be closed by the caller.
func ForwardRequest(ctx context.Context, in *http.Request, match *RouteMatch, upstreamBase string) (*http.Response, error) {
	return ForwardRequestWithConfig(ctx, in, match, upstreamBase, DefaultUpstreamCADir)
}

// ForwardRequestWithConfig forwards an incoming request with upstream TLS configuration.
// The TLS configuration is extracted from the route's TLSConfig field.
func ForwardRequestWithConfig(ctx context.Context, in *http.Request, match *RouteMatch, upstreamBase string, upstreamCADir string) (*http.Response, error) {
	if in == nil {
		return nil, fmt.Errorf("incoming request is nil")
	}
	if in.URL == nil {
		return nil, fmt.Errorf("incoming request URL is nil")
	}
	// Validate the upstream base URL format
	_, err := parseUpstreamBaseURL(upstreamBase)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Extract TLS configuration from the route
	var tlsConfig *UpstreamTLSConfig
	if match != nil && match.Route.TLSConfig != nil {
		tlsConfig = match.Route.TLSConfig
	}

	// Build client with route-specific TLS configuration
	client, err := newUpstreamHTTPClientWithTLS(tlsConfig, upstreamCADir)
	if err != nil {
		return nil, fmt.Errorf("build upstream HTTP client: %w", err)
	}

	return forwardRequestWithClient(ctx, in, match, upstreamBase, client, upstreamRequestTimeout)
}

func forwardRequestWithClient(ctx context.Context, in *http.Request, match *RouteMatch, upstreamBase string, client *http.Client, timeout time.Duration) (*http.Response, error) {
	if in == nil {
		return nil, fmt.Errorf("incoming request is nil")
	}
	if in.URL == nil {
		return nil, fmt.Errorf("incoming request URL is nil")
	}
	baseURL, err := parseUpstreamBaseURL(upstreamBase)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = upstreamRequestTimeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)

	outboundURL := buildUpstreamURL(baseURL, in.URL.Path, in.URL.RawQuery)
	if match != nil {
		path, pathErr := ComputeUpstreamPath(match)
		if pathErr != nil {
			cancel()
			return nil, pathErr
		}
		outboundURL = buildUpstreamURLWithEscapedPath(baseURL, path, in.URL.RawQuery)
	}
	outboundReq, err := http.NewRequestWithContext(requestCtx, in.Method, outboundURL, in.Body)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("creating upstream request: %w", err)
	}
	copyRequestMetadata(in, outboundReq)

	if client == nil {
		client = defaultUpstreamClient
	}
	resp, err := client.Do(outboundReq)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("upstream request failed: %w", err)
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

func parseUpstreamBaseURL(rawURL string) (*url.URL, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		if err == nil {
			err = fmt.Errorf("URL must include a scheme and host")
		}
		return nil, fmt.Errorf("invalid upstream base URL: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("invalid upstream base URL: unsupported scheme %q", parsedURL.Scheme)
	}
	if port := parsedURL.Port(); port != "" {
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return nil, fmt.Errorf("invalid upstream base URL: invalid port %q", port)
		}
	}
	parsedURL.Fragment = ""
	parsedURL.RawFragment = ""
	return parsedURL, nil
}

// buildUpstreamURL appends path to the upstream base path and replaces the
// base query with the incoming request query.
func buildUpstreamURL(upstreamURL *url.URL, requestPath, rawQuery string) string {
	if upstreamURL == nil {
		return ""
	}
	target := *upstreamURL
	target.Path = joinURLPaths(upstreamURL.Path, requestPath)
	target.RawPath = ""
	target.RawQuery = rawQuery
	target.Fragment = ""
	target.RawFragment = ""
	return target.String()
}

func buildUpstreamURLWithEscapedPath(upstreamURL *url.URL, requestPath, rawQuery string) string {
	if upstreamURL == nil {
		return ""
	}
	decodedPath, err := url.PathUnescape(requestPath)
	if err != nil {
		return ""
	}
	target := *upstreamURL
	target.Path = joinURLPaths(upstreamURL.Path, decodedPath)
	target.RawPath = joinURLPaths(upstreamURL.EscapedPath(), requestPath)
	if target.RawPath == target.Path {
		target.RawPath = ""
	}
	target.RawQuery = rawQuery
	target.ForceQuery = false
	target.Fragment = ""
	target.RawFragment = ""
	return target.String()
}

func joinURLPaths(basePath, requestPath string) string {
	if requestPath == "" {
		requestPath = "/"
	}
	if basePath == "" || basePath == "/" {
		if strings.HasPrefix(requestPath, "/") {
			return requestPath
		}
		return "/" + requestPath
	}
	return strings.TrimRight(basePath, "/") + "/" + strings.TrimLeft(requestPath, "/")
}

func (p *ReverseProxy) streamResponse(w http.ResponseWriter, body io.Reader) error {
	if p.BufferPool == nil {
		p.BufferPool = newBufferPool()
	}
	buf := p.BufferPool.Get()
	defer p.BufferPool.Put(buf)
	_, err := io.CopyBuffer(w, body, buf)
	return err
}

// StreamResponse writes status, headers, and streams the response body to the
// caller. It handles hop-by-hop header filtering, ensures the response body is
// closed, and returns an error if writing fails.
func StreamResponse(w http.ResponseWriter, resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("response is nil")
	}

	// Copy headers, excluding hop-by-hop headers.
	copyHeaders(resp.Header, w.Header())

	// Write status code
	w.WriteHeader(resp.StatusCode)

	// Stream the response body efficiently using io.Copy
	// The Go http package handles chunked transfer encoding automatically
	_, err := io.Copy(w, resp.Body)
	if err != nil {
		return fmt.Errorf("streaming response body: %w", err)
	}

	// Always close the response body to release the connection
	if closeErr := resp.Body.Close(); closeErr != nil {
		return fmt.Errorf("closing response body: %w", closeErr)
	}

	return nil
}

func (p *ReverseProxy) handleError(w http.ResponseWriter, r *http.Request, err error, phase string) {
	log.Printf("[proxy] Error during %s: %v", phase, err)
	if responseStarted(w) {
		return
	}

	code := ErrCodeUpstreamFailed
	message := "Upstream request failed"
	if vault.IsSecretStoreUnavailable(err) {
		code = ErrCodeServiceUnavailable
		message = "Secret store unavailable"
		if retryAfter := vault.RetryAfterSeconds(err); retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		}
	}
	NewErrorResponse(code, message).Write(w, r)
}

// responseWriterTracker records whether a response has been committed so an
// error from a streaming response cannot append a second response body.
type responseWriterTracker struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *responseWriterTracker) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *responseWriterTracker) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *responseWriterTracker) ResponseStarted() bool {
	return w.wroteHeader
}

func (w *responseWriterTracker) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func responseStarted(w http.ResponseWriter) bool {
	tracker, ok := w.(interface{ ResponseStarted() bool })
	return ok && tracker.ResponseStarted()
}

// bufferPool manages buffers for copying response bodies.
type bufferPool struct {
	bufSize int
}

func newBufferPool() *bufferPool {
	return &bufferPool{bufSize: 32 * 1024}
}

func (p *bufferPool) Get() []byte {
	return make([]byte, p.bufSize)
}

func (p *bufferPool) Put(_ []byte) {}

// cancelOnClose keeps the request context alive while the caller streams the
// response, then releases its timer as soon as the body is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (b *cancelOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.cancel)
	return err
}

// SetUpstreamURL updates the upstream URL for the proxy.
func (p *ReverseProxy) SetUpstreamURL(upstreamURL string) error {
	parsedURL, err := parseUpstreamBaseURL(upstreamURL)
	if err != nil {
		return err
	}
	p.UpstreamURL = parsedURL
	p.UpstreamHost = parsedURL.Host
	return nil
}

// GetUpstreamURL returns the current upstream URL.
func (p *ReverseProxy) GetUpstreamURL() string {
	if p != nil && p.UpstreamURL != nil {
		return p.UpstreamURL.String()
	}
	return ""
}

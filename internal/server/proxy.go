package server

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	upstreamConnectTimeout  = 5 * time.Second
	upstreamRequestTimeout  = 30 * time.Second
	upstreamMaxIdleConns    = 100
	upstreamIdleConnTimeout = 90 * time.Second
)

// defaultUpstreamClient is shared by standalone ForwardRequest calls. Sharing
// the client also shares its Transport, so requests to the same upstream can
// reuse pooled connections.
var defaultUpstreamClient = newUpstreamHTTPClient()

// newUpstreamHTTPClient creates the client used for outbound upstream calls.
// The dial and TLS handshake limits bound connection establishment while the
// client timeout bounds the complete request, including waiting for a response.
func newUpstreamHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: upstreamConnectTimeout, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          upstreamMaxIdleConns,
		MaxIdleConnsPerHost:   upstreamMaxIdleConns,
		IdleConnTimeout:       upstreamIdleConnTimeout,
		TLSHandshakeTimeout:   upstreamConnectTimeout,
		ResponseHeaderTimeout: upstreamRequestTimeout,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   upstreamRequestTimeout,
	}
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
}

// NewReverseProxy creates a reverse proxy for upstreamURL.
func NewReverseProxy(upstreamURL string) (*ReverseProxy, error) {
	parsedURL, err := parseUpstreamBaseURL(upstreamURL)
	if err != nil {
		return nil, err
	}

	return &ReverseProxy{
		Client:         defaultUpstreamClient,
		UpstreamURL:    parsedURL,
		UpstreamHost:   parsedURL.Host,
		RequestTimeout: upstreamRequestTimeout,
		BufferPool:     newBufferPool(),
	}, nil
}

// ServeHTTP implements http.Handler for the reverse proxy.
func (p *ReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	outgoingURL := p.buildUpstreamURL(r)
	upstreamReq, err := p.buildUpstreamRequest(ctx, r, outgoingURL)
	if err != nil {
		p.handleError(w, r, err, "building upstream request")
		return
	}

	if err := p.dispatchAndServe(ctx, w, r, upstreamReq); err != nil {
		p.handleError(w, r, err, "upstream request")
	}
}

// buildUpstreamURL constructs the full upstream URL from the base URL and the
// incoming request path and query.
func (p *ReverseProxy) buildUpstreamURL(r *http.Request) string {
	if p == nil || p.UpstreamURL == nil || r == nil || r.URL == nil {
		return ""
	}
	return buildUpstreamURL(p.UpstreamURL, r.URL.Path, r.URL.RawQuery)
}

// buildUpstreamRequest creates the request sent to the upstream service.
func (p *ReverseProxy) buildUpstreamRequest(ctx context.Context, inboundReq *http.Request, outgoingURL string) (*http.Request, error) {
	if inboundReq == nil {
		return nil, fmt.Errorf("incoming request is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	outboundReq, err := http.NewRequestWithContext(ctx, inboundReq.Method, outgoingURL, inboundReq.Body)
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

// isHopByHopHeader reports whether header is one of the standard hop-by-hop
// headers. Connection-listed headers are handled by hopByHopHeaders, where
// the complete source header set is available.
func isHopByHopHeader(header string) bool {
	_, ok := hopByHopHeaders(nil)[http.CanonicalHeaderKey(header)]
	return ok
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
	return forwardRequestWithClient(ctx, in, match, upstreamBase, defaultUpstreamClient, upstreamRequestTimeout)
}

func forwardRequestWithClient(ctx context.Context, in *http.Request, _ *RouteMatch, upstreamBase string, client *http.Client, timeout time.Duration) (*http.Response, error) {
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

func (p *ReverseProxy) copyHeaders(inbound, outbound http.Header, _ string) {
	copyHeaders(inbound, outbound)
}

func (p *ReverseProxy) setForwardedHeaders(inboundReq, outboundReq *http.Request) {
	setForwardedHeaders(inboundReq, outboundReq)
}

func (p *ReverseProxy) copyResponseHeaders(upstream, caller http.Header) {
	copyHeaders(upstream, caller)
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

	// Copy headers, excluding hop-by-hop headers
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

func (p *ReverseProxy) handleError(w http.ResponseWriter, _ *http.Request, err error, phase string) {
	log.Printf("[proxy] Error during %s: %v", phase, err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	_, _ = fmt.Fprintf(w, `{"error":"bad_gateway","message":"Upstream request failed: %s"}`, err.Error())
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

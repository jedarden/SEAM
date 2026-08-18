package server

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

const (
	// DefaultMaxBufferedResponseBytes bounds the response held in memory for
	// decode, scrub, and re-encode. Responses larger than this are scrubbed
	// incrementally; the limit is never a scrubbability limit.
	DefaultMaxBufferedResponseBytes int64 = 1 * 1024 * 1024

	// RedactedSecret is deliberately a fixed, recognizable marker. It contains
	// no part of the value being removed.
	RedactedSecret = "[REDACTED-BY-SEAM]"
)

var errUnscannableResponse = errors.New("upstream response cannot be scanned for injected credentials")

type responseScrubContextKey struct{}

type responseScrubConfig struct {
	secrets          [][]byte
	allowUnscannable bool
}

func withResponseScrub(ctx context.Context, secret []byte, allowUnscannable bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	config, _ := ctx.Value(responseScrubContextKey{}).(*responseScrubConfig)
	if config == nil {
		config = &responseScrubConfig{}
	} else {
		copyConfig := *config
		copyConfig.secrets = append([][]byte(nil), config.secrets...)
		config = &copyConfig
	}
	if len(secret) > 0 {
		config.secrets = append(config.secrets, bytes.Clone(secret))
	}
	config.allowUnscannable = config.allowUnscannable || allowUnscannable
	return context.WithValue(ctx, responseScrubContextKey{}, config)
}

func responseScrubFromContext(ctx context.Context) *responseScrubConfig {
	if ctx == nil {
		return nil
	}
	config, _ := ctx.Value(responseScrubContextKey{}).(*responseScrubConfig)
	return config
}

// ScrubBytes replaces every injected value using a leftmost, longest match.
// Matching is byte-oriented so a secret split at any UTF-8 or chunk boundary
// is treated exactly like a secret in an ordinary text body.
func ScrubBytes(body []byte, secrets ...[]byte) []byte {
	scrubber := newSecretScrubber(secrets)
	return scrubber.redact(body)
}

// ScrubReader copies src to dst while replacing secrets. It retains enough
// overlap to make a match spanning arbitrary Read calls indistinguishable
// from a match in one contiguous buffer.
func ScrubReader(dst io.Writer, src io.Reader, secrets ...[]byte) error {
	if dst == nil || src == nil {
		return fmt.Errorf("scrub reader requires non-nil source and destination")
	}
	return newSecretScrubber(secrets).stream(dst, src)
}

type secretScrubber struct {
	secrets [][]byte
	overlap int
	marker  []byte
}

func newSecretScrubber(secrets [][]byte) *secretScrubber {
	clean := make([][]byte, 0, len(secrets))
	seen := make(map[string]struct{}, len(secrets))
	for _, secret := range secrets {
		if len(secret) == 0 {
			continue
		}
		key := string(secret)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		clean = append(clean, bytes.Clone(secret))
	}
	sort.SliceStable(clean, func(i, j int) bool {
		return len(clean[i]) > len(clean[j])
	})
	overlap := 0
	for _, secret := range clean {
		if len(secret) > overlap {
			overlap = len(secret)
		}
	}
	if overlap > 0 {
		overlap--
	}
	return &secretScrubber{secrets: clean, overlap: overlap, marker: []byte(RedactedSecret)}
}

func (s *secretScrubber) redact(body []byte) []byte {
	if s == nil || len(s.secrets) == 0 || len(body) == 0 {
		return bytes.Clone(body)
	}

	// The common case has no match. Avoid an allocation for it while retaining
	// the copy-on-write behavior expected by callers of ScrubBytes.
	matched := false
	for i := 0; i < len(body) && !matched; i++ {
		for _, secret := range s.secrets {
			if bytes.HasPrefix(body[i:], secret) {
				matched = true
				break
			}
		}
	}
	if !matched {
		return bytes.Clone(body)
	}

	result := make([]byte, 0, len(body))
	for i := 0; i < len(body); {
		var match []byte
		for _, secret := range s.secrets {
			if bytes.HasPrefix(body[i:], secret) {
				match = secret
				break
			}
		}
		if len(match) == 0 {
			result = append(result, body[i])
			i++
			continue
		}
		result = append(result, s.marker...)
		i += len(match)
	}
	return result
}

func (s *secretScrubber) stream(dst io.Writer, src io.Reader) error {
	if s == nil || len(s.secrets) == 0 {
		_, err := io.Copy(dst, src)
		return err
	}

	buf := make([]byte, 32*1024)
	pending := make([]byte, 0, s.overlap+len(buf))
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			redacted, consumed := s.scrubSafePrefix(pending)
			if consumed > 0 {
				if err := writeAll(dst, redacted); err != nil {
					return err
				}
				if flusher, ok := dst.(interface{ Flush() }); ok {
					flusher.Flush()
				}
				pending = append([]byte(nil), pending[consumed:]...)
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return writeAll(dst, s.redact(pending))
			}
			return readErr
		}
	}
}

// scrubSafePrefix scans through the safe portion using the same leftmost,
// longest-match rule as redact. It examines the retained overlap too, so an
// occurrence that crosses the boundary is held from its beginning. Scanning
// rather than blindly redacting body[:boundary] matters for overlapping
// occurrences such as the secret "00" in a run of zeroes.
func (s *secretScrubber) scrubSafePrefix(body []byte) ([]byte, int) {
	if len(body) <= s.overlap {
		return nil, 0
	}
	boundary := len(body) - s.overlap
	output := make([]byte, 0, boundary)
	offset := 0
	for offset < boundary {
		var match []byte
		for _, secret := range s.secrets {
			if bytes.HasPrefix(body[offset:], secret) {
				match = secret
				break
			}
		}
		if len(match) == 0 {
			output = append(output, body[offset])
			offset++
			continue
		}
		if offset+len(match) > boundary {
			break
		}
		output = append(output, s.marker...)
		offset += len(match)
	}
	return output, offset
}

func writeAll(dst io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := dst.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func parseContentEncodings(header string) ([]string, error) {
	if strings.TrimSpace(header) == "" {
		return nil, nil
	}
	parts := strings.Split(header, ",")
	encodings := make([]string, 0, len(parts))
	for _, part := range parts {
		encoding := strings.ToLower(strings.TrimSpace(part))
		if encoding == "" {
			return nil, fmt.Errorf("empty content encoding")
		}
		switch encoding {
		case "identity", "gzip", "deflate":
			encodings = append(encodings, encoding)
		default:
			return nil, fmt.Errorf("unsupported content encoding %q", encoding)
		}
	}
	return encodings, nil
}

func decodeBody(body io.Reader, encodings []string) (io.Reader, []io.Closer, error) {
	current := body
	closers := make([]io.Closer, 0, len(encodings))
	for i := len(encodings) - 1; i >= 0; i-- {
		switch encodings[i] {
		case "identity":
			continue
		case "gzip":
			reader, err := gzip.NewReader(current)
			if err != nil {
				closeAll(closers)
				return nil, nil, fmt.Errorf("decode gzip response: %w", err)
			}
			current = reader
			closers = append(closers, reader)
		case "deflate":
			reader, err := zlib.NewReader(current)
			if err != nil {
				closeAll(closers)
				return nil, nil, fmt.Errorf("decode deflate response: %w", err)
			}
			current = reader
			closers = append(closers, reader)
		}
	}
	return current, closers, nil
}

func closeAll(closers []io.Closer) error {
	var first error
	for i := len(closers) - 1; i >= 0; i-- {
		if err := closers[i].Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func encodeBytes(body []byte, encodings []string) ([]byte, error) {
	var output bytes.Buffer
	writer, closeWriter, err := encodedWriter(&output, encodings)
	if err != nil {
		return nil, err
	}
	if err := writeAll(writer, body); err != nil {
		_ = closeWriter()
		return nil, err
	}
	if err := closeWriter(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func encodedWriter(dst io.Writer, encodings []string) (io.Writer, func() error, error) {
	current := dst
	closers := make([]io.Closer, 0, len(encodings))
	for i := len(encodings) - 1; i >= 0; i-- {
		switch encodings[i] {
		case "identity":
			continue
		case "gzip":
			writer := gzip.NewWriter(current)
			current = writer
			closers = append(closers, writer)
		case "deflate":
			writer := zlib.NewWriter(current)
			current = writer
			closers = append(closers, writer)
		default:
			return nil, nil, fmt.Errorf("unsupported content encoding %q", encodings[i])
		}
	}
	return current, func() error { return closeAll(closers) }, nil
}

func responseIsOpaque(resp *http.Response) bool {
	if resp == nil {
		return true
	}
	if resp.StatusCode == http.StatusSwitchingProtocols || resp.Header.Get("Upgrade") != "" {
		return true
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	}
	mediaType = strings.ToLower(mediaType)
	if strings.HasPrefix(mediaType, "text/") || strings.HasPrefix(mediaType, "message/") {
		return false
	}
	if strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml") ||
		mediaType == "application/json" || mediaType == "application/xml" ||
		mediaType == "application/javascript" || mediaType == "application/graphql" ||
		mediaType == "application/x-www-form-urlencoded" || mediaType == "application/x-ndjson" ||
		mediaType == "application/ndjson" {
		return false
	}
	return strings.HasPrefix(mediaType, "image/") || strings.HasPrefix(mediaType, "audio/") ||
		strings.HasPrefix(mediaType, "video/") || strings.HasPrefix(mediaType, "application/")
}

func scrubHeaderValues(headers http.Header, scrubber *secretScrubber) http.Header {
	if headers == nil {
		return nil
	}
	result := headers.Clone()
	for name, values := range result {
		for i, value := range values {
			values[i] = string(scrubber.redact([]byte(value)))
		}
		result[name] = values
	}
	return result
}

func trailerNames(header http.Header, trailers http.Header) []string {
	seen := make(map[string]struct{})
	names := make([]string, 0)
	add := func(name string) {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
		if canonical == "" || canonical == "Trailer" {
			return
		}
		if _, ok := seen[canonical]; ok {
			return
		}
		seen[canonical] = struct{}{}
		names = append(names, canonical)
	}
	for _, value := range header.Values("Trailer") {
		for _, name := range strings.Split(value, ",") {
			add(name)
		}
	}
	for name := range trailers {
		add(name)
	}
	sort.Strings(names)
	return names
}

func copyResponseHeaders(w http.ResponseWriter, headers http.Header, scrubber *secretScrubber, trailers http.Header) {
	if scrubber != nil {
		headers = scrubHeaderValues(headers, scrubber)
	}
	copyHeaders(headers, w.Header())
	if names := trailerNames(headers, trailers); len(names) > 0 {
		w.Header().Set("Trailer", strings.Join(names, ", "))
	}
}

func copyResponseTrailers(w http.ResponseWriter, trailers http.Header, scrubber *secretScrubber) {
	for name, values := range trailers {
		if scrubber != nil {
			values = scrubHeaderValues(http.Header{name: values}, scrubber)[name]
		}
		w.Header()[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
	}
}

func (s *secretScrubber) streamResponse(w http.ResponseWriter, resp *http.Response, maxBuffered int64) error {
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	if maxBuffered <= 0 {
		maxBuffered = DefaultMaxBufferedResponseBytes
	}
	encodings, err := parseContentEncodings(resp.Header.Get("Content-Encoding"))
	if err != nil {
		return err
	}
	decoded, closers, err := decodeBody(resp.Body, encodings)
	if err != nil {
		return err
	}
	defer func() { _ = closeAll(closers) }()

	copyResponseHeaders(w, resp.Header, s, resp.Trailer)
	if resp.ContentLength >= 0 && resp.ContentLength <= maxBuffered {
		body, readErr := io.ReadAll(io.LimitReader(decoded, maxBuffered+1))
		if readErr != nil {
			return fmt.Errorf("reading buffered response: %w", readErr)
		}
		if int64(len(body)) <= maxBuffered {
			body = s.redact(body)
			body, err = encodeBytes(body, encodings)
			if err != nil {
				return err
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(resp.StatusCode)
			if err := writeAll(w, body); err != nil {
				return err
			}
			copyResponseTrailers(w, resp.Trailer, s)
			return nil
		}
		decoded = io.MultiReader(bytes.NewReader(body), decoded)
	}

	// Oversized and unknown-length bodies use the same incremental scrubber.
	// Encode after scrubbing so the caller receives the same content-encoding.
	writer, closeWriter, err := encodedWriter(w, encodings)
	if err != nil {
		return err
	}
	w.Header().Del("Content-Length")
	w.WriteHeader(resp.StatusCode)
	streamErr := s.stream(writer, decoded)
	closeErr := closeWriter()
	if streamErr != nil {
		return streamErr
	}
	if closeErr != nil {
		return closeErr
	}
	copyResponseTrailers(w, resp.Trailer, s)
	return nil
}

func (s *secretScrubber) serveUnscannable(w http.ResponseWriter, resp *http.Response) error {
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	copyResponseHeaders(w, resp.Header, nil, resp.Trailer)
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		return err
	}
	copyResponseTrailers(w, resp.Trailer, nil)
	return nil
}

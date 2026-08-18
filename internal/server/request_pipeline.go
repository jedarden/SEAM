package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// InjectionKind is the wire location used for a route credential.
type InjectionKind string

const (
	InjectionHeader InjectionKind = "header"
	InjectionBearer InjectionKind = "bearer"
	InjectionQuery  InjectionKind = "query"
)

// InjectAs describes how a fetched credential is presented upstream. Bearer
// injection deliberately has no name: its destination is Authorization.
type InjectAs struct {
	Kind InjectionKind
	Name string
}

func (i *InjectAs) validate() error {
	if i == nil {
		return nil
	}
	switch i.Kind {
	case InjectionHeader, InjectionQuery:
		if i.Name == "" {
			return fmt.Errorf("x-inject-as %q injection requires name", i.Kind)
		}
	case InjectionBearer:
		if i.Name != "" {
			return fmt.Errorf("x-inject-as bearer injection must not have name")
		}
	default:
		return fmt.Errorf("unsupported x-inject-as kind %q", i.Kind)
	}
	return nil
}

// InjectSecret applies one credential after inbound sanitation. It is kept
// independent of the secret store so callers can test the security boundary
// without ever putting a credential value in route metadata.
func InjectSecret(req *http.Request, injectAs *InjectAs, secret []byte) error {
	if req == nil || req.URL == nil {
		return fmt.Errorf("request is nil")
	}
	if err := injectAs.validate(); err != nil {
		return err
	}
	if injectAs == nil {
		return nil
	}
	if len(secret) == 0 {
		return fmt.Errorf("injected secret is empty")
	}

	value := string(secret)
	switch injectAs.Kind {
	case InjectionHeader:
		deleteHeaderFold(req.Header, injectAs.Name)
		req.Header.Set(injectAs.Name, value)
	case InjectionBearer:
		deleteHeaderFold(req.Header, "Authorization")
		req.Header.Set("Authorization", "Bearer "+value)
	case InjectionQuery:
		// Sanitation removes every caller occurrence. Keep all unrelated raw
		// query bytes untouched and append exactly one encoded credential. Do
		// the same-name removal here too so this helper remains safe when used
		// outside the normal RouteTable.Match pipeline.
		req.URL.RawQuery = stripQueryNames(req.URL.RawQuery, map[string]bool{injectAs.Name: true})
		if req.URL.RawQuery != "" {
			req.URL.RawQuery += "&"
		}
		req.URL.RawQuery += url.QueryEscape(injectAs.Name) + "=" + url.QueryEscape(value)
	}
	return nil
}

// SanitizeRequest applies the stage-2 strip rule. Header names are compared
// case-insensitively and all duplicate map entries are deleted. Query names
// are compared exactly after query decoding, while unrelated raw query fields
// retain their original order and spelling.
func (t *RouteTable) SanitizeRequest(req *http.Request) error {
	if req == nil || req.URL == nil {
		return fmt.Errorf("request is nil")
	}

	headerNames, queryNames := t.injectableNames(req)
	for name := range req.Header {
		lower := strings.ToLower(name)
		if lower == "authorization" || (strings.HasPrefix(lower, "x-seam-") &&
			lower != "x-seam-dry-run" && lower != "x-seam-api-version") ||
			headerNames[lower] {
			delete(req.Header, name)
		}
	}
	req.URL.RawQuery = stripQueryNames(req.URL.RawQuery, queryNames)
	return nil
}

func (t *RouteTable) injectableNames(req *http.Request) (map[string]bool, map[string]bool) {
	headers := make(map[string]bool)
	queries := make(map[string]bool)
	if t == nil || req == nil || req.URL == nil {
		return headers, queries
	}
	for _, route := range t.matchingRoutes(req) {
		for name := range route.injectableHeaderNames() {
			headers[strings.ToLower(name)] = true
		}
		for name := range route.injectableQueryNames() {
			queries[name] = true
		}
	}
	return headers, queries
}

func stripQueryNames(rawQuery string, names map[string]bool) string {
	if rawQuery == "" || len(names) == 0 {
		return rawQuery
	}
	parts := strings.Split(rawQuery, "&")
	kept := parts[:0]
	for _, part := range parts {
		key := part
		if equal := strings.IndexByte(part, '='); equal >= 0 {
			key = part[:equal]
		}
		decoded, err := url.QueryUnescape(key)
		if err == nil && names[decoded] {
			continue
		}
		kept = append(kept, part)
	}
	return strings.Join(kept, "&")
}

func deleteHeaderFold(headers http.Header, wanted string) {
	for name := range headers {
		if strings.EqualFold(name, wanted) {
			delete(headers, name)
		}
	}
}

type routeMatchContextKey struct{}

type routeSecretResolver func(context.Context, RouteEntry) ([]byte, error)

func withRouteMatch(req *http.Request, match *RouteMatch, resolver routeSecretResolver) {
	if req == nil {
		return
	}
	ctx := context.WithValue(req.Context(), routeMatchContextKey{}, match)
	if resolver != nil {
		ctx = context.WithValue(ctx, routeSecretResolverContextKey{}, resolver)
	}
	*req = *req.WithContext(ctx)
}

type routeSecretResolverContextKey struct{}

func routeMatchFromRequest(req *http.Request) *RouteMatch {
	if req == nil {
		return nil
	}
	match, _ := req.Context().Value(routeMatchContextKey{}).(*RouteMatch)
	return match
}

func routeSecretResolverFromRequest(req *http.Request) routeSecretResolver {
	if req == nil {
		return nil
	}
	resolver, _ := req.Context().Value(routeSecretResolverContextKey{}).(routeSecretResolver)
	return resolver
}

// ComputeUpstreamPath implements the four-step path rule after matching:
// start from the template, remove the designated instance segment, apply the
// explicit template or strip-prefix rewrite, then substitute each binding as
// one encoded segment.
func ComputeUpstreamPath(match *RouteMatch) (string, error) {
	if match == nil {
		return "", fmt.Errorf("route match is nil")
	}
	route := match.Route
	pathTemplate := route.PathTemplate
	if route.InstanceParam != "" {
		parts := strings.Split(pathTemplate, "/")
		filtered := make([]string, 0, len(parts))
		instanceSegment := "{" + route.InstanceParam + "}"
		for _, part := range parts {
			if part == instanceSegment {
				continue
			}
			filtered = append(filtered, part)
		}
		pathTemplate = strings.Join(filtered, "/")
	}

	if route.UpstreamPathTemplate != "" {
		pathTemplate = route.UpstreamPathTemplate
	} else if route.UpstreamStripPrefix != "" {
		if pathTemplate != route.UpstreamStripPrefix {
			prefix := strings.TrimSuffix(route.UpstreamStripPrefix, "/")
			if !strings.HasPrefix(pathTemplate, prefix+"/") {
				return "", fmt.Errorf("upstream strip prefix %q does not apply to path %q", route.UpstreamStripPrefix, pathTemplate)
			}
			pathTemplate = strings.TrimPrefix(pathTemplate, prefix)
		}
	}
	if pathTemplate == "" {
		pathTemplate = "/"
	}
	if !strings.HasPrefix(pathTemplate, "/") {
		return "", fmt.Errorf("upstream path %q must start with '/'", pathTemplate)
	}

	var output strings.Builder
	for i := 0; i < len(pathTemplate); {
		if pathTemplate[i] != '{' {
			output.WriteByte(pathTemplate[i])
			i++
			continue
		}
		end := strings.IndexByte(pathTemplate[i+1:], '}')
		if end < 0 {
			return "", fmt.Errorf("unclosed path parameter in %q", pathTemplate)
		}
		end += i + 1
		name := pathTemplate[i+1 : end]
		value, ok := match.PathParams[name]
		if !ok {
			return "", fmt.Errorf("upstream path parameter %q has no binding", name)
		}
		output.WriteString(encodePathBinding(value))
		i = end + 1
	}
	path := output.String()
	if path == "" {
		return "/", nil
	}
	return path, nil
}

func encodePathBinding(value string) string {
	const hex = "0123456789ABCDEF"
	var output strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		// Dot is escaped intentionally, including a single dot, so neither a
		// binding of ".." nor any dot segment can be interpreted by a URL hop.
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '~' {
			output.WriteByte(c)
			continue
		}
		output.WriteByte('%')
		output.WriteByte(hex[c>>4])
		output.WriteByte(hex[c&15])
	}
	return output.String()
}

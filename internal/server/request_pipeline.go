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
	InjectionHeader   InjectionKind = "header"
	InjectionBearer   InjectionKind = "bearer"
	InjectionQuery    InjectionKind = "query"
	InjectionTemplate InjectionKind = "template"
	// InjectionOAuthRefresh stores a refresh token in the secret store and
	// injects the short-lived bearer obtained by exchanging it at TokenURL.
	InjectionOAuthRefresh InjectionKind = "oauth-refresh"
)

// InjectAs describes how a fetched credential is presented upstream. Bearer
// injection deliberately has no name: its destination is Authorization.
type InjectAs struct {
	Kind InjectionKind
	Name string
	// Prefix is used by template injection. It is deliberately configuration,
	// never part of the stored credential value (for example, "Token ").
	Prefix string
	// The following fields make a list entry self-contained when a route needs
	// more than one credential. Empty values inherit the route-level source.
	CredentialSource string
	VaultPath        string
	CredentialEnv    string
	CredentialFile   string
	// oauth-refresh only.
	TokenURL   string
	ClientID   string
	TokenField string
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
	case InjectionOAuthRefresh:
		if i.Name != "" {
			return fmt.Errorf("x-inject-as oauth-refresh injection must not have name")
		}
		if u, err := url.Parse(i.TokenURL); err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("x-inject-as oauth-refresh requires an https tokenUrl")
		}
		if i.ClientID == "" {
			return fmt.Errorf("x-inject-as oauth-refresh requires clientId")
		}
	case InjectionTemplate:
		if i.Name == "" {
			return fmt.Errorf("x-inject-as template injection requires name")
		}
		if i.Prefix == "" {
			return fmt.Errorf("x-inject-as template injection requires prefix")
		}
	default:
		return fmt.Errorf("unsupported x-inject-as kind %q", i.Kind)
	}
	if i.CredentialSource != "" && i.CredentialSource != credentialSourceVault && i.CredentialSource != credentialSourceEnv && i.CredentialSource != credentialSourceFile {
		return fmt.Errorf("x-inject-as source %q is not supported", i.CredentialSource)
	}
	if i.CredentialEnv != "" && (i.CredentialSource != "" && i.CredentialSource != credentialSourceEnv) {
		return fmt.Errorf("x-inject-as env source requires source: env")
	}
	if i.CredentialFile != "" && (i.CredentialSource != "" && i.CredentialSource != credentialSourceFile) {
		return fmt.Errorf("x-inject-as file source requires source: file")
	}
	if i.VaultPath != "" && (i.CredentialSource != "" && i.CredentialSource != credentialSourceVault) {
		return fmt.Errorf("x-inject-as vault source requires source: vault")
	}
	switch i.CredentialSource {
	case credentialSourceVault:
		if i.VaultPath == "" {
			return fmt.Errorf("x-inject-as source: vault requires vaultPath")
		}
		if i.CredentialEnv != "" || i.CredentialFile != "" {
			return fmt.Errorf("x-inject-as vault source cannot include env or file reference")
		}
	case credentialSourceEnv:
		if i.CredentialEnv == "" || i.VaultPath != "" || i.CredentialFile != "" {
			return fmt.Errorf("x-inject-as source: env requires credentialEnv only")
		}
	case credentialSourceFile:
		if i.CredentialFile == "" || i.VaultPath != "" || i.CredentialEnv != "" {
			return fmt.Errorf("x-inject-as source: file requires credentialFile only")
		}
	}
	return nil
}

// StaticInjection describes a non-secret constant written to an upstream
// request. Static values never enter the response scrub union.
type StaticInjection struct {
	Kind  InjectionKind
	Name  string
	Value string
}

func (i *StaticInjection) validate() error {
	if i == nil {
		return nil
	}
	if i.Kind != InjectionHeader && i.Kind != InjectionQuery {
		return fmt.Errorf("x-inject-static kind %q must be header or query", i.Kind)
	}
	if i.Name == "" {
		return fmt.Errorf("x-inject-static %q injection requires name", i.Kind)
	}
	return nil
}

func (i *InjectAs) headerName() string {
	if i == nil {
		return ""
	}
	switch i.Kind {
	case InjectionBearer, InjectionOAuthRefresh:
		return "Authorization"
	case InjectionHeader, InjectionTemplate:
		return i.Name
	default:
		return ""
	}
}

func (i *InjectAs) queryName() string {
	if i != nil && i.Kind == InjectionQuery {
		return i.Name
	}
	return ""
}

// InjectSecret applies one credential after inbound sanitation. It is kept
// independent of the secret store so callers can test the security boundary
// without ever putting a credential value in route metadata.
func InjectSecret(req *http.Request, injectAs *InjectAs, secret []byte) error {
	return InjectSecrets(req, []*InjectAs{injectAs}, [][]byte{secret})
}

// InjectSecrets applies several credentials after inbound sanitation. The
// slices must have matching lengths; a secret is resolved for each entry so a
// route can use distinct vault, env, or file references.
func InjectSecrets(req *http.Request, injectAs []*InjectAs, secrets [][]byte) error {
	if req == nil || req.URL == nil {
		return fmt.Errorf("request is nil")
	}
	if len(injectAs) != len(secrets) {
		return fmt.Errorf("injection and secret counts differ: %d != %d", len(injectAs), len(secrets))
	}
	for index, entry := range injectAs {
		if entry == nil {
			continue
		}
		if err := entry.validate(); err != nil {
			return err
		}
		if len(secrets[index]) == 0 {
			return fmt.Errorf("injected secret is empty")
		}
		value := string(secrets[index])
		switch entry.Kind {
		case InjectionHeader:
			deleteHeaderFold(req.Header, entry.Name)
			req.Header.Set(entry.Name, value)
		case InjectionBearer, InjectionOAuthRefresh:
			deleteHeaderFold(req.Header, "Authorization")
			req.Header.Set("Authorization", "Bearer "+value)
		case InjectionTemplate:
			deleteHeaderFold(req.Header, entry.Name)
			req.Header.Set(entry.Name, entry.Prefix+value)
		case InjectionQuery:
			// Sanitation removes every caller occurrence. Keep all unrelated raw
			// query bytes untouched and append exactly one encoded credential.
			req.URL.RawQuery = stripQueryNames(req.URL.RawQuery, map[string]bool{entry.Name: true})
			if req.URL.RawQuery != "" {
				req.URL.RawQuery += "&"
			}
			req.URL.RawQuery += url.QueryEscape(entry.Name) + "=" + url.QueryEscape(value)
		}
	}
	return nil
}

// InjectStatic applies non-secret constants after sanitation.
func InjectStatic(req *http.Request, injections []*StaticInjection) error {
	if req == nil || req.URL == nil {
		return fmt.Errorf("request is nil")
	}
	for _, injection := range injections {
		if injection == nil {
			continue
		}
		if err := injection.validate(); err != nil {
			return err
		}
		switch injection.Kind {
		case InjectionHeader:
			deleteHeaderFold(req.Header, injection.Name)
			req.Header.Set(injection.Name, injection.Value)
		case InjectionQuery:
			req.URL.RawQuery = stripQueryNames(req.URL.RawQuery, map[string]bool{injection.Name: true})
			if req.URL.RawQuery != "" {
				req.URL.RawQuery += "&"
			}
			req.URL.RawQuery += url.QueryEscape(injection.Name) + "=" + url.QueryEscape(injection.Value)
		}
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
		for name := range route.staticHeaderNames() {
			headers[strings.ToLower(name)] = true
		}
		for name := range route.staticQueryNames() {
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

type routeCredentialSetResolver interface {
	ResolveAll(context.Context, RouteEntry) ([][]byte, error)
	RefreshableAll(context.Context, RouteEntry) ([][]byte, error)
}

func resolveRouteCredentials(ctx context.Context, resolver CredentialSource, route RouteEntry) ([][]byte, error) {
	if multi, ok := resolver.(routeCredentialSetResolver); ok {
		return multi.ResolveAll(ctx, route)
	}
	secret, err := resolver.Resolve(ctx, route)
	if err != nil {
		return nil, err
	}
	return [][]byte{secret}, nil
}

func refreshRouteCredentials(ctx context.Context, resolver CredentialSource, route RouteEntry) ([][]byte, error) {
	if multi, ok := resolver.(routeCredentialSetResolver); ok {
		return multi.RefreshableAll(ctx, route)
	}
	secret, err := resolver.Refreshable(ctx, route)
	if err != nil {
		return nil, err
	}
	return [][]byte{secret}, nil
}

type legacyCredentialSource routeSecretResolver

func (f legacyCredentialSource) Resolve(ctx context.Context, route RouteEntry) ([]byte, error) {
	return f(ctx, route)
}

func (f legacyCredentialSource) Refreshable(ctx context.Context, route RouteEntry) ([]byte, error) {
	return f(ctx, route)
}

func withRouteMatch(req *http.Request, match *RouteMatch, resolver any) {
	if req == nil {
		return
	}
	ctx := context.WithValue(req.Context(), routeMatchContextKey{}, match)
	if resolver != nil {
		var source CredentialSource
		switch value := resolver.(type) {
		case CredentialSource:
			source = value
		case routeSecretResolver:
			source = legacyCredentialSource(value)
		case func(context.Context, RouteEntry) ([]byte, error):
			source = legacyCredentialSource(value)
		}
		if source != nil {
			ctx = context.WithValue(ctx, routeSecretResolverContextKey{}, source)
		}
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

func routeSecretResolverFromRequest(req *http.Request) CredentialSource {
	if req == nil {
		return nil
	}
	resolver, _ := req.Context().Value(routeSecretResolverContextKey{}).(CredentialSource)
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

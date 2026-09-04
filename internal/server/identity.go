package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
)

// Identity represents a resolved caller identity from Tailscale WhoIs
type Identity struct {
	// NodeKey is the stable Tailscale node key
	NodeKey string `json:"node_key"`

	// NodeName is the hostname of the calling node
	NodeName string `json:"node_name"`

	// User is the user identity (if human)
	User string `json:"user,omitempty"`

	// Tags are the Tailscale tags associated with the node
	Tags []string `json:"tags,omitempty"`

	// Capabilities are the scope claims from the Grant's app field
	Capabilities []string `json:"capabilities,omitempty"`

	// Resolved indicates whether identity resolution succeeded
	Resolved bool `json:"resolved"`
}

// contextKey type for storing identity in request context
type identityContextKey struct{}

// contextWithIdentity stores the resolved identity in the request context
func contextWithIdentity(ctx context.Context, identity *Identity) context.Context {
	return context.WithValue(ctx, identityContextKey{}, identity)
}

// identityFromContext extracts the resolved identity from the request context
func identityFromContext(ctx context.Context) *Identity {
	if identity, ok := ctx.Value(identityContextKey{}).(*Identity); ok {
		return identity
	}
	return nil
}

// IdentityResolver resolves inbound connections to Tailscale identities
type IdentityResolver struct {
	// TODO: Add Tailscale LocalClient for WhoIs calls
	// For now, this is a placeholder that will be integrated with Tailscale
	mu     sync.RWMutex
	testMode bool // When true, returns resolved test identities for development

	// resolveOverride, when set, replaces Tailscale resolution entirely and is
	// handed the caller's remote address. Tests install it where there is no
	// tailnet address to resolve — a suite driving a real listener over
	// loopback can never present one — and production leaves it nil, so the
	// non-tailnet default-deny below is untouched. Only test code can set it:
	// the setter lives in identity_resolution_test.go, so the production binary
	// carries the field and the branch that honors it but no way to arm it.
	resolveOverride func(remoteAddr string) (*Identity, error)
}

// NewIdentityResolver creates a new identity resolver
func NewIdentityResolver() *IdentityResolver {
	// Check for test mode via environment variable
	// SEAM_TEST_IDENTITY_MODE=1 enables resolved test identities for development
	testMode := os.Getenv("SEAM_TEST_IDENTITY_MODE") == "1"

	return &IdentityResolver{
		testMode: testMode,
	}
}

// Resolve resolves an inbound connection to a Tailscale identity
// This is the Stage 3 implementation: WhoIs on the inbound connection
func (ir *IdentityResolver) Resolve(ctx context.Context, remoteAddr string) (*Identity, error) {
	// Parse the remote address
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		// If it's already a host (no port), use it as-is
		host = remoteAddr
	}

	ir.mu.RLock()
	defer ir.mu.RUnlock()

	// A test override stands in for WhoIs resolution in its entirety.
	if fn := ir.resolveOverride; fn != nil {
		return fn(remoteAddr)
	}

	// TODO: Integrate with Tailscale LocalClient.WhoIs
	// For now, this is a placeholder that will be replaced with actual WhoIs resolution

	// Placeholder: Check if this is a Tailscale IP (100.x.x.x)
	ip := net.ParseIP(host)
	if ip != nil && isTailscaleIP(ip) {
		// This is a Tailscale connection
		if ir.testMode {
			// Test mode: return resolved identity with test scopes
			// This allows development to proceed while waiting for LocalClient integration
			return &Identity{
				Resolved:     true,
				NodeName:     "test-worker",
				NodeKey:      "test-node-key",
				User:         "test-user@example.com",
				Tags:         []string{"tag:needle-worker"},
				Capabilities: []string{"k8s-ro:get", "argocd:read", "config:read", "seam:ops:read", "seam:scopes:read-all"},
			}, nil
		}

		// Production mode: return unresolved identity (will be denied by middleware)
		// Return unresolved identity for now
		return &Identity{
			Resolved:     false,
			NodeName:     host,
			NodeKey:      host, // Placeholder
		}, nil
	}

	// Not a Tailscale IP
	return &Identity{
		Resolved: false,
		NodeName: host,
	}, fmt.Errorf("not a Tailscale IP address")
}

// isTailscaleIP checks if an IP is a Tailscale IP (100.x.x.x or CGNAT)
func isTailscaleIP(ip net.IP) bool {
	if ip == nil {
		return false
	}

	// Tailscale uses 100.x.x.x range
	if ip4 := ip.To4(); ip4 != nil {
		// 100.64.0.0/10 - CGNAT (used by Tailscale)
		if ip4[0] == 100 && (ip4[1]&0xc0) == 64 {
			return true
		}
	}

	return false
}

// ResolveFromRequest resolves identity from an HTTP request
// Extracts the remote address and resolves it via WhoIs
func (ir *IdentityResolver) ResolveFromRequest(r *http.Request) (*Identity, error) {
	if r == nil {
		return nil, fmt.Errorf("request is nil")
	}

	// Get remote address from request
	remoteAddr := r.RemoteAddr
	if remoteAddr == "" {
		return nil, fmt.Errorf("no remote address in request")
	}

	return ir.Resolve(r.Context(), remoteAddr)
}

// ExtractScopeClaims extracts scope claims from a Tailscale identity
// This reads from the Grant's app capability field
// Format: tailscale.com/cap/seam-scopes = ["k8s-ro:get", "argocd:read"]
func ExtractScopeClaims(identity *Identity) []string {
	if identity == nil || !identity.Resolved {
		return nil
	}

	// For now, return capabilities from the identity
	// TODO: Parse these from the actual Grant's app field
	// When LocalClient integration is complete, this will parse WhoIs response
	return identity.Capabilities
}

// HasTag checks if an identity has a specific Tailscale tag
func (id *Identity) HasTag(tag string) bool {
	if id == nil {
		return false
	}

	for _, t := range id.Tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}
	return false
}

// HasScope checks if an identity has a specific scope claim
func (id *Identity) HasScope(scope string) bool {
	if id == nil {
		return false
	}

	for _, s := range id.Capabilities {
		if strings.EqualFold(s, scope) {
			return true
		}
	}
	return false
}

// String returns a string representation of the identity
func (id *Identity) String() string {
	if id == nil {
		return "identity:nil"
	}

	if !id.Resolved {
		return fmt.Sprintf("identity:unresolved(%s)", id.NodeName)
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("node=%s", id.NodeName))

	if id.User != "" {
		parts = append(parts, fmt.Sprintf("user=%s", id.User))
	}

	if len(id.Tags) > 0 {
		parts = append(parts, fmt.Sprintf("tags=%v", id.Tags))
	}

	if len(id.Capabilities) > 0 {
		parts = append(parts, fmt.Sprintf("scopes=%v", id.Capabilities))
	}

	return fmt.Sprintf("identity:%s", strings.Join(parts, ","))
}

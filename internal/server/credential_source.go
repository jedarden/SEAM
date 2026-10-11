package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"

	"github.com/ardenone/seam/internal/vault"
)

// CredentialSource resolves a route credential without exposing its backing
// store to the request pipeline. Refreshable is the forced-read path used
// after an upstream rejects a credential.
type CredentialSource interface {
	Resolve(context.Context, RouteEntry) ([]byte, error)
	Refreshable(context.Context, RouteEntry) ([]byte, error)
}

// ErrCredentialSourceNotRefreshable is returned by sources that cannot replace
// a credential after an upstream 401.
var ErrCredentialSourceNotRefreshable = errors.New("credential source is not refreshable")

const (
	credentialSourceVault  = "vault"
	credentialSourceEnv    = "env"
	credentialSourceFile   = "file"
	fileCredentialMountDir = "/var/run/seam/credentials"
	maxCredentialFileBytes = 1 << 20
)

var credentialFileNamePattern = regexp.MustCompile(`^SEAM_CRED_[A-Z0-9_]+$`)

// envCredentialSource is an immutable process-start snapshot. Secret values
// are never formatted or logged; the only value-derived log field is length.
type envCredentialSource struct {
	values map[string][]byte
	errors map[string]error
}

func (s *envCredentialSource) hasReference(name string) bool {
	if s == nil {
		return false
	}
	_, hasValue := s.values[name]
	_, hasError := s.errors[name]
	return hasValue || hasError
}

func newEnvCredentialSource(names []string) *envCredentialSource {
	source := &envCredentialSource{
		values: make(map[string][]byte, len(names)),
		errors: make(map[string]error),
	}
	unique := make(map[string]struct{}, len(names))
	ordered := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := unique[name]; ok || name == "" {
			continue
		}
		unique[name] = struct{}{}
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		value, ok := os.LookupEnv(name)
		if !ok {
			source.errors[name] = fmt.Errorf("credential environment variable %q is not set", name)
			continue
		}
		trimmed := bytes.TrimRight([]byte(value), "\r\n")
		if len(trimmed) == 0 {
			source.errors[name] = fmt.Errorf("credential environment variable %q is empty after trimming CR/LF", name)
			continue
		}
		source.values[name] = bytes.Clone(trimmed)
		log.Printf("[CredentialSource] loaded env credential (%d bytes)", len(trimmed))
	}
	return source
}

func (s *envCredentialSource) Resolve(_ context.Context, route RouteEntry) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("env credential source is nil")
	}
	if route.CredentialSource != credentialSourceEnv || route.CredentialEnv == "" {
		return nil, fmt.Errorf("route has no env credential reference")
	}
	if err := s.errors[route.CredentialEnv]; err != nil {
		return nil, err
	}
	value, ok := s.values[route.CredentialEnv]
	if !ok {
		return nil, fmt.Errorf("credential environment variable %q was not captured at process start", route.CredentialEnv)
	}
	return bytes.Clone(value), nil
}

func (s *envCredentialSource) Refreshable(context.Context, RouteEntry) ([]byte, error) {
	return nil, ErrCredentialSourceNotRefreshable
}

// fileCredentialSource reads a key from the operator-owned Secret volume.
// It deliberately opens the path for each resolution: Kubernetes atomically
// switches the volume's ..data symlink on Secret updates, so the next request
// or sentinel probe sees one complete old or new payload without a watcher
// race or a process restart.
type fileCredentialSource struct {
	dir string
}

func newFileCredentialSource(dir string) *fileCredentialSource {
	if dir == "" {
		dir = fileCredentialMountDir
	}
	return &fileCredentialSource{dir: dir}
}

func (s *fileCredentialSource) Resolve(_ context.Context, route RouteEntry) (resolved []byte, retErr error) {
	if s == nil {
		return nil, fmt.Errorf("file credential source is nil")
	}
	name := route.CredentialFile
	if route.CredentialSource != credentialSourceFile || !credentialFileNamePattern.MatchString(name) {
		return nil, fmt.Errorf("route has no valid file credential reference")
	}
	path := filepath.Join(s.dir, name)
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("credential file %q is unavailable: %w", name, err)
	}
	defer func() {
		if err := file.Close(); err != nil && retErr == nil {
			resolved = nil
			retErr = fmt.Errorf("close credential file %q: %w", name, err)
		}
	}()

	before, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat credential file %q: %w", name, err)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("credential file %q is not a regular file", name)
	}
	value, err := io.ReadAll(io.LimitReader(file, maxCredentialFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read credential file %q: %w", name, err)
	}
	if len(value) > maxCredentialFileBytes {
		return nil, fmt.Errorf("credential file %q exceeds %d bytes", name, maxCredentialFileBytes)
	}
	after, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("restat credential file %q: %w", name, err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, fmt.Errorf("credential file %q changed while being read", name)
	}
	value = bytes.TrimRight(value, "\r\n")
	if len(value) == 0 {
		return nil, fmt.Errorf("credential file %q is empty after trimming CR/LF", name)
	}
	return bytes.Clone(value), nil
}

// Refreshable reopens the projected path, allowing a Secret rotation to
// replace a credential rejected with 401 before SEAM retries the request.
func (s *fileCredentialSource) Refreshable(ctx context.Context, route RouteEntry) ([]byte, error) {
	return s.Resolve(ctx, route)
}

type credentialCacheStatsProvider interface {
	CacheStats() vault.CacheStats
}

type credentialLoginSource interface {
	Login(context.Context) error
}

// vaultCredentialSource adapts the existing OpenBao client to the generic
// source contract. It preserves the old cache, hold-down, eager invalidation,
// and OAuth-refresh behavior while keeping all vault client calls here.
type vaultCredentialSource struct {
	client      *vault.Client
	mu          sync.Mutex
	oauthTokens *oauthTokenCache
}

func newVaultCredentialSource(client *vault.Client) *vaultCredentialSource {
	return &vaultCredentialSource{client: client}
}

// newVaultCredentialSourceFromLegacyClient keeps the old in-package metrics
// test hook working without exposing the vault client type to request-routing
// code. The adapter is deliberately kept with the vault implementation so all
// OpenBao client knowledge remains behind CredentialSource.
func newVaultCredentialSourceFromLegacyClient(client any) *vaultCredentialSource {
	vaultClient, ok := client.(*vault.Client)
	if !ok || vaultClient == nil {
		return nil
	}
	return newVaultCredentialSource(vaultClient)
}

func newVaultCredentialSourceFromEnv() (*vaultCredentialSource, error) {
	client, err := vault.NewFromEnv()
	if err != nil {
		return nil, err
	}
	return newVaultCredentialSource(client), nil
}

func (s *vaultCredentialSource) Resolve(ctx context.Context, route RouteEntry) ([]byte, error) {
	return s.resolve(ctx, route, false)
}

func (s *vaultCredentialSource) Refreshable(ctx context.Context, route RouteEntry) ([]byte, error) {
	return s.resolve(ctx, route, true)
}

func (s *vaultCredentialSource) resolve(ctx context.Context, route RouteEntry, force bool) ([]byte, error) {
	if s == nil || s.client == nil {
		return nil, fmt.Errorf("OpenBao credential source is nil")
	}
	if route.VaultPath == "" {
		return nil, fmt.Errorf("route has no x-vault-path")
	}
	var (
		secret vault.Secret
		err    error
	)
	if force {
		secret, err = s.client.RefreshAfterUnauthorized(ctx, route.VaultPath)
	} else {
		secret, err = s.client.GetSecret(ctx, route.VaultPath)
	}
	if err != nil {
		return nil, err
	}
	stored, err := credentialValue(secret)
	if err != nil || route.InjectAs == nil || route.InjectAs.Kind != InjectionOAuthRefresh {
		return stored, err
	}

	s.mu.Lock()
	if s.oauthTokens == nil {
		s.oauthTokens = newOAuthTokenCache()
	}
	cache := s.oauthTokens
	s.mu.Unlock()
	bearer, err := cache.bearer(ctx, route.InjectAs, string(stored), force)
	if err != nil {
		return nil, err
	}
	return []byte(bearer), nil
}

func (s *vaultCredentialSource) CacheStats() vault.CacheStats {
	if s == nil {
		return vault.CacheStats{}
	}
	return s.client.CacheStats()
}

func (s *vaultCredentialSource) Login(ctx context.Context) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("OpenBao credential source is nil")
	}
	return s.client.Login(ctx)
}

func credentialValue(secret vault.Secret) ([]byte, error) {
	for _, key := range []string{"value", "token", "secret", "api_key", "api-key", "key"} {
		if value, ok := secret[key]; ok {
			return secretValueString(value)
		}
	}
	if len(secret) == 1 {
		for _, value := range secret {
			return secretValueString(value)
		}
	}
	return nil, fmt.Errorf("secret has no usable credential field")
}

func secretValueString(value any) ([]byte, error) {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil, fmt.Errorf("secret credential is empty")
		}
		return []byte(typed), nil
	case []byte:
		if len(typed) == 0 {
			return nil, fmt.Errorf("secret credential is empty")
		}
		return append([]byte(nil), typed...), nil
	default:
		return nil, fmt.Errorf("secret credential is not a string")
	}
}

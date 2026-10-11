# Credential source interface

Phase 15 places every runtime credential read behind the server's
`CredentialSource` interface:

```go
type CredentialSource interface {
    Resolve(context.Context, RouteEntry) ([]byte, error)
    Refreshable(context.Context, RouteEntry) ([]byte, error)
}
```

`Resolve` is used for ordinary request injection and sentinel probes.
`Refreshable` is the forced-read path after an upstream 401; a source must
invalidate any cached value before returning the replacement. The existing
OpenBao client is adapted by the vault source, which also owns the existing
OAuth refresh-token exchange and cache. The env source snapshots each declared
process variable into memory when the route table is built, trims trailing
CR/LF bytes, and does not support refresh after an upstream 401.

An env fragment declares `x-credential-source: env`,
`x-credential-env: SEAM_CRED_<OWNER>_<NAME>`, and `x-inject-as`. The owner
namespace comes from the parent directory's `x-seam-owner`, upper-cased with
hyphens changed to underscores. The name suffix matches `[A-Z0-9_]+`. This
prevents a fragment from reaching another owner's value or using SEAM's
`SEAM_OPENBAO_*` / `SEAM_VAULT_*` configuration. Pass the value to the process
from a Kubernetes Secret; it never appears in logs, and a 401 is surfaced
without an in-place retry while the sentinel marks the configured probe
unhealthy.

Existing fragments keep the legacy vault behavior: `x-vault-path` with
`x-inject-as` selects vault when `x-credential-source` is omitted. Explicit
`vault` and `env` source declarations are accepted, and `x-inject-as` is
present exactly when one credential source is declared.

The source is injectable in tests through `RouteTable.SetCredentialSource` and
`ProbeLoopConfig.CredentialSource`. The request context retains a compatibility
adapter for the existing function-based proxy tests while production wiring
uses the interface directly.

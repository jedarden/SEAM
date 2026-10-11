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

The file source receives only an owner-scoped filename. Its mount directory is
fixed by SEAM at `/var/run/seam/credentials` and by the gateway Deployment; a
fragment cannot select a directory or path. Each `Resolve` opens the mounted
path anew instead of caching a value or depending on an inotify event. A
Kubernetes Secret volume switches its `..data` symlink atomically, so an
in-flight request keeps the complete revision it opened while the next
request or probe reads the new revision. Empty files and detected in-place
changes during a read fail closed. `Refreshable` uses the same fresh read, so
a 401 can retry with a rotated file value.

An env fragment declares `x-credential-source: env`,
`x-credential-env: SEAM_CRED_<OWNER>_<NAME>`, and `x-inject-as`. The owner
namespace comes from the parent directory's `x-seam-owner`, upper-cased with
hyphens changed to underscores. The name suffix matches `[A-Z0-9_]+`. This
prevents a fragment from reaching another owner's value or using SEAM's
`SEAM_OPENBAO_*` / `SEAM_VAULT_*` configuration. Pass the value to the process
from a Kubernetes Secret; it never appears in logs, and a 401 is surfaced
without an in-place retry while the sentinel marks the configured probe
unhealthy.

A file fragment declares `x-credential-source: file`,
`x-credential-file: SEAM_CRED_<OWNER>_<NAME>`, and `x-inject-as`. The value is
the Secret key's filename and must follow the same upper-cased owner prefix
and `[A-Z0-9_]+` suffix rule as an env reference. `seam lint` and runtime
fragment validation enforce the rule, including more-specific sibling owners.
The Deployment mounts the separate `seam-provider-file-credentials` Secret
read-only at SEAM's fixed directory. Reloader watches the env-backed Secret
for restarts and does not watch this file-source Secret, so projected file
updates reach the running process without a rollout.
File values are never logged; the resolved bytes feed the same injection,
response-scrub, 401-refresh, and sentinel-probe paths as other sources.

Existing fragments keep the legacy vault behavior: `x-vault-path` with
`x-inject-as` selects vault when `x-credential-source` is omitted. Explicit
`vault`, `env`, and `file` source declarations are accepted, and `x-inject-as` is
present exactly when one credential source is declared.

The source is injectable in tests through `RouteTable.SetCredentialSource` and
`ProbeLoopConfig.CredentialSource`. The request context retains a compatibility
adapter for the existing function-based proxy tests while production wiring
uses the interface directly.

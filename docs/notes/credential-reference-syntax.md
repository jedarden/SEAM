# The canonical credential-reference syntax

SEAM names a credential in exactly two places, and each place has exactly one
legal shape. This document is the definition of both shapes, the boundary
between them, and the rejection classes that keep them from being confused.
Where prose elsewhere (READMEs, schema docs, design notes) describes a
credential reference, it defers to this page; if a doc disagrees with this
page, this page wins and the doc is the bug.

## One credential, two surfaces

A credential lives in OpenBao at a KV v2 path. With mount `secret` (the
default; `SEAM_OPENBAO_MOUNT` overrides it), the physical read is
`GET <SEAM_OPENBAO_ADDR>/v1/secret/data/<P>` for logical path `P`. The two
surfaces that ever name `P`:

| Surface | Field | Canonical shape | Example |
|---|---|---|---|
| Route fragment (deployment config) | `x-vault-path` | bare path, **no scheme** | `rs-manager/rs-manager/seam/routes/argocd-ro/ro-token` |
| Differential corpus (test fixture) | `secrets[].ref` | **`vault:` scheme + path** | `vault:rs-manager/rs-manager/seam/routes/argocd-ro/ro-token` |

The scheme is the boundary marker. A fragment's `x-vault-path` is read by the
gateway itself, in a context where nothing else could be meant, so it carries
no scheme. A corpus ref doubles as a lookup key in operator-held secrets
sources — a JSON file and a set of environment variables — where a bare path
could be mistaken for a literal value, so it is stamped `vault:` to mark the
string as a *reference, never a value*. The two forms are not interchangeable
in either direction, and both directions of confusion are rejected:

- **A `vault:`-prefixed `x-vault-path` is an ambiguous reference** — a corpus
  ref pasted into a fragment. The gateway refuses it at fragment load
  (`vault_path_carries_scheme`, `internal/spec.AllowlistEnforcer.ValidateVaultPath`);
  it never strips the prefix and tries, because silently "fixing" a reference
  would also silently accept a fragment authored by someone who did not
  understand which surface they were writing for.
- **A scheme-less corpus `ref` is equally ambiguous** — a bare path pasted
  where the resolver keys by ref. The corpus loader rejects it
  (`does not carry the vault: scheme`). There is no default-scheme fallback:
  a ref either names its scheme or is not a reference.

## Fragment surface: `x-vault-path`

```yaml
# declarative-config/k8s/rs-manager/seam/routes/<owner>/<name>.yaml
x-seam-owner: argocd-ro
x-vault-path: rs-manager/rs-manager/seam/routes/argocd-ro/ro-token
x-inject-as:
  kind: bearer
```

Syntax rules, in the order the gateway applies them:

1. **Scheme-less bare path.** No `vault:` prefix (see above). Enforced at
   fragment load by `ValidateVaultPath` (`internal/spec/allowlist.go`).
2. **No traversal.** `..` and backslash separators are rejected outright
   (`vault_path_contains_traversal`), before any path arithmetic runs.
3. **No globs.** `*`, `?`, `[` are rejected (`vault_path_contains_globs`).
4. **No templated segments.** `{`/`}` (OpenAPI-style parameters) are rejected
   (`vault_path_contains_templates`).
5. **Owner-nested under the base.** The path must land inside
   `<base>/<x-seam-owner>/`, where `base` is deployment configuration:
   `SEAM_VAULT_BASE_DIR`, default `rs-manager/rs-manager/seam/routes`
   (`internal/spec.ResolveVaultBaseDir`). A path outside the owner's
   directory is refused (`vault_path_outside_owner_directory`); the fragment
   loader turns any of the above into a load failure, so a bad path fails the
   deployment, not the first request.
6. **Paired with injection metadata.** `x-vault-path` and `x-inject-as`
   ({kind: header|bearer|query, name}) are present together or not at all —
   enforced by the lint (`upstream-map.vault-inject-pairing` for per-entry
   maps) and the fragment schema.

At request time `internal/vault` re-canonicalizes the path
(`vault.ResolvePath`: leading `/` trimmed, `.`/`..`/empty segments and
`*?[]{}$()` rejected) and reads `<mount>/data/<P>`, caching by the canonical
path.

Two static layers mirror the runtime rule, deliberately weaker:

- **Lint** (`internal/spec/lint.go`, `vaultPathCoOwned`) checks only the
  *relative* shape — the owner appears as an interior segment with something
  above it (the base) and a secret name below. The base itself is never named
  in lint, so a base move is a deployment-config change, not a lint change.
- **Fixture examples** under `docs/notes/fragments/` carry full canonical
  paths so a new fragment is copied from a correct shape.

## Corpus surface: `secrets[].ref`

```json
{
  "secrets": [
    {
      "ref": "vault:rs-manager/rs-manager/seam/routes/argocd-ro/ro-token",
      "injectAs": {"kind": "bearer"}
    }
  ]
}
```

Syntax rules:

1. **`vault:` scheme required**, followed by a non-empty path.
2. **The same rejection classes as the fragment surface**: traversal (`..`,
   backslash), globs, templated segments.
3. **Base containment, boundary-correct.** The path after the scheme must
   resolve *strictly inside* the enforced base — the same
   `SEAM_VAULT_BASE_DIR` default the gateway enforces, resolved by the
   harness from the environment. The containment check is a `filepath.Rel`
   test, not a string prefix, so `seam/routes2/x` is not "under"
   `seam/routes`, the bare base itself is refused (direct access to the
   parent), and the pre-consolidation base `seam/routes/*` is rejected as
   outside the enforced prefix. A ref that fails this is rejected **at corpus
   load**, while the corpus is still a local fixture — never at replay, where
   it would surface as a resolution failure mid-run.
4. **The ref names the path relative to the mount**, exactly like the
   fragment surface — the corpus ref for the credential a fragment reads at
   `x-vault-path: P` is `vault:P`, nothing more re-written.

### Serialization: refs travel, values never

- A corpus stores the **ref only**. The resolved value rides in
  `Secret.Bare`, tagged `json:"-"`, so no marshal path in the harness — save,
  report, capture — can ever persist it.
- The operator-side value sources are git-ignored by convention
  (`*.local.json`) and hold **ref → value** maps keyed by the exact ref
  string.
- The corpus fixture that ships in the repo carries refs that are valid
  *shapes*; their values exist only in an operator's local file.

### Resolution at replay: file over env

`tools/diffharness/internal/secref` resolves a ref at replay time, in memory:

1. **File leg** (`--secrets`): a JSON object mapping the *exact ref string*
   to the value. Exact-match keys make this leg unambiguous by construction.
2. **Env leg**: `SEAM_DIFF_SECRET_` + the ref sanitized (uppercased, every
   run of non-`[A-Z0-9_]` collapsed to one `_` — the whole ref including the
   scheme):
   `vault:seam/routes/argocd/ro-token` →
   `SEAM_DIFF_SECRET_VAULT_SEAM_ROUTES_ARGOCD_RO_TOKEN`.

A ref neither source holds marks the entry **SKIP** ("unresolved secret
ref"), not FAIL — an unresolved ref is a configuration gap on the operator
side, not a cutover regression, and must not turn a corpus red.

**Known non-injectivity of the env leg.** Sanitization folds `/`, `_`, and
`-` to the same underscore, so distinct refs can collide on one variable
(`vault:a/b`, `vault:a_b`, and `vault:a-b` all derive
`SEAM_DIFF_SECRET_VAULT_A_B`). The env leg is a convenience, not the
disambiguator: when a corpus carries refs that sanitize identically, resolve
them through the file leg, whose keys are the exact ref strings. The
sanitization mapping and this collision class are pinned by
`tools/diffharness/internal/secref` tests.

## Rejection classes at a glance

| Rejected input | Class | Corpus loader | Fragment loader |
|---|---|---|---|
| `ref` without `vault:` | ambiguous: bare path where a ref is required | reject (`vault: scheme`) | n/a |
| `x-vault-path` with `vault:` | ambiguous: ref pasted into a fragment | n/a | reject (`vault_path_carries_scheme`) |
| empty ref / empty path | names nothing | reject | no-op (absent path = no credential) |
| `..` anywhere, `\` separators | traversal | reject | reject |
| `*`, `?`, `[` | glob | reject | reject |
| `{`, `}` | templated segment | reject | reject |
| path outside `<base>/<owner>` | off-base / off-owner | reject (boundary-correct `Rel`) | reject |
| the bare base itself | parent-directory access | reject | reject |

Every rejection fires at **load** — fragment load for the gateway, corpus
load for the harness — so a mis-shaped reference is a fixture/deployment bug
caught before any request is replayed, never a runtime surprise.

## Enforcement points

| Rule | Where |
|---|---|
| `x-vault-path` scheme + traversal + globs + templates + owner nesting | `internal/spec/allowlist.go` `AllowlistEnforcer.ValidateVaultPath` |
| base resolution (`SEAM_VAULT_BASE_DIR`, default) | `internal/spec/allowlist.go` `ResolveVaultBaseDir`; mirrored standalone in `tools/diffharness/internal/corpus` |
| relative co-ownership (lint, base-agnostic) | `internal/spec/lint.go` `vaultPathCoOwned` |
| request-time path canonicalization + KV read | `internal/vault/vault.go` `ResolvePath`, `Client.GetSecret` |
| corpus `ref` shape + base containment | `tools/diffharness/internal/corpus` loader |
| ref → value resolution, file-over-env, env naming | `tools/diffharness/internal/secref` |

## Related

- `docs/notes/route-fragment-schema.md` — the fragment schema `x-vault-path`
  sits in.
- `tools/diffharness/README.md` — replay workflow, secrets-file format, the
  ref→env mapping examples.
- `docs/capture_testing.md` — capture-side redaction and corpus integrity
  checks.
- `docs/design/argocd-ro-corpus-data-structure.md` — the corpus data model
  the ref field lives in.

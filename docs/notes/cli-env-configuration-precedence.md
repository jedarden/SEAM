# CLI / environment configuration precedence

Beads: seam-cb79e4f0 (the contract and its first tests), seam-465bb1ff
(committed this note and reconciled it with 6f3e077, which flipped serve's
direction after an earlier draft of this note was written). The README
documented `SEAM_*` variables and flags side by side without saying which
wins or what an invalid value does. This note is the rationale; the
normative statement lives in the README ("Precedence (serve)" / "Invalid
values"), and the tests pin it.

## The contract, in one line per command

| Command | Precedence | Variables |
|---|---|---|
| `serve` | **flag over environment** | all 15 in the README table |
| `healthcheck` | **flag over environment** | `SEAM_CALLER_PORT` only |
| `lint`, `diff` | **flag over environment** (env fills a flag still at its default) | `SEAM_FRAGMENTS_DIR`, `SEAM_SCHEMA_PATH`, `SEAM_UPSTREAM_ALLOWLIST` |
| `import` | no `SEAM_*` configuration input | — |

Empty string counts as unset everywhere.

## Why serve is flag-over-environment

The environment is the deployment-time surface and the flag is the explicit,
per-invocation expression of intent, so the flag wins and the environment
fills what the invocation does not name (README, "Precedence (serve)"): an
operator gets environment-based deployment defaults without losing the CLI
override. An earlier draft of this note argued the opposite — the container
entrypoint's flags are image-level, so a Deployment's env should not be
silently defeated by whatever the image bakes in — and the first
implementation matched that. Commit 6f3e077 flipped serve to
flag-over-environment, using explicit flag-set tracking (`flagWasSet` over
`fs.Visit`) rather than a default-value sentinel, which is what let serve
adopt the rule without the ambiguity lint/diff still carry. Whichever
direction you think is right, the one that ships is the one the README
states; if it ever flips again, update the README, this note and the tests
below in the same commit.

`seam healthcheck` resolves `SEAM_CALLER_PORT` with the same rule serve
does: an explicit `--caller-port` wins, an environment-only override is
honoured. They must agree, or a Deployment's port override sends the
kubelet's probe at a listener serve never bound (or misses one it did).

## Why lint/diff are flag-over-environment

They are developer tools: a typed flag is the more specific intent, so it
wins, and the environment is a convenience that fills the default. The
implementation compares against the default sentinel (`fragmentsDir ==
"./fragments"`), which carries a known limitation: passing the default value
explicitly is indistinguishable from omitting the flag, so the environment
still fills it. Serve solved the same problem with the `flagWasSet`
explicit-set helper; lint/diff could adopt it the same way if the sentinel
ever bites. Until then the limitation is documented rather than hidden
(the README states the corollary explicitly).

## Value parsing, and why the sharp edges are pinned rather than fixed

Integer variables parse with `fmt.Sscanf(val, "%d", ...)`, whose exact
semantics are part of the contract:

- leading whitespace is skipped;
- trailing junk is ignored — `"8080abc"` configures `8080`;
- `"0x10"` configures `0` (`%d` takes the decimal prefix);
- negative and out-of-range values are applied unchecked and fail later, when
  the listener binds;
- a value with no leading integer is rejected: previous value kept, warning
  logged.

These are pinned by tests (not "fixed" to stricter parsing) because deployed
Deployments may already rely on the lenient reading, and a range check at
configuration time would change which failures surface and where. If SEAM
ever moves to `strconv.ParseInt` with explicit range validation, the tests
named below are the ones to update in the same commit.

Boolean variables recognize exactly `true` and `1`, lowercase. When no
explicit flag was passed, any other non-empty value — including `TRUE`,
`yes`, `0` and `false` — supplies false for `SEAM_FRAGMENT_MODE` and
`SEAM_CAPTURE_ENABLED`, and is inert for `SEAM_HOT_RELOAD_ENABLED`. An
explicit flag beats every environment value, so the environment can never
turn off a feature the invocation switched on. `SEAM_HOT_RELOAD_ENABLED` is
deliberately asymmetric — only `true`/`1` acts, so the environment can turn
hot reload on but never off — which is how it shipped; unifying it would
change behavior for any Deployment that sets the variable to something
non-boolean.

## Where it is pinned

- `cmd/seam/main_test.go` — `TestServeFlagOverridesEnv`,
  `TestServeEmptyEnvValueKeepsFlag`, `TestServePortEnvParsing`,
  `TestServeByteLimitEnvParsing`, `TestServeBooleanEnvForms`,
  `TestServeInvalidIntegerEnvLogsWarning`,
  `TestResolveHealthcheckCallerPort`, `TestServeDefaultsWithoutFlagsOrEnv`.
  They drive `registerServeFlags` + `applyEnvOverrides` — extracted from
  `serveCommand` for exactly this, the way `runHealthcheck` was split out of
  `healthcheckCommand` — so the tested wiring is the binary's wiring, and the
  flag defaults have a single source of truth.
- `cmd/seam/main_test.go` `TestResolveVaultBaseDir` — the vault-base-dir
  override (flag over environment, whitespace-trim, shared default).
- `cmd/seam/lint_command_test.go` — `TestLintEnvSuppliesDefaultFragmentsDir`,
  `TestLintExplicitFlagBeatsEnvFragmentsDir`,
  `TestLintEnvSuppliesDefaultSchemaPath`,
  `TestLintExplicitFlagBeatsEnvSchemaPath`,
  `TestLintEnvSuppliesDefaultAllowlistPath`,
  `TestLintExplicitFlagBeatsEnvAllowlistPath`. The allowlist pair completes
  the trio the table above names for lint: it is also the one variable whose
  *absence* is meaningful (inert before Phase 6a, authoritative once
  supplied), so the pair's first phase pins that a set-but-empty variable
  stays inert rather than resolving to a path.
- `cmd/seam/diff_command_test.go` — `TestDiffEnvSuppliesDefaultFragmentsDir`,
  `TestDiffExplicitFlagBeatsEnvFragmentsDir`.

The lint/diff tests read real exit codes off real fragment fixtures, so they
also pin that the resolved path is the one the command actually reads.

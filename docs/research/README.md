# Research Index

Four research threads, conducted 2026-07-16 via parallel subagents, informing SEAM's core design and its near-future per-agent tool scoping requirement.

- [`secret-injection-gateways.md`](secret-injection-gateways.md) — prior art for the core proxy+inject function (Vault Agent, Boundary, Teleport, oauth2-proxy, Kong/Tyk/KrakenD, Secretless Broker, Infisical Agent Vault).
- [`agent-tool-access-control.md`](agent-tool-access-control.md) — state of the art for scoping which tools an LLM agent can call (MCP gateways, Agent IAM vendors, multi-agent framework enforcement, OAuth2 scope conventions).
- [`tailscale-identity-mechanisms.md`](tailscale-identity-mechanisms.md) — whether Tailscale's own Grants/WhoIs/tsnet primitives can supply per-agent identity instead of a custom auth layer.
- [`needle-identity-model.md`](needle-identity-model.md) — what identity/credential concept NEEDLE (the agent worker fleet) currently gives to spawned agent sessions (spoiler: none).

## Combined recommendation

The four threads converge on one coherent design for per-agent tool scoping, which the core proxy+inject objective doesn't need but the near-future requirement does:

1. **Identity:** each NEEDLE worker/agent gets its own `tsnet`-embedded ephemeral Tailscale node (ADR: don't build SPIFFE/SPIRE or a custom PKI — Tailscale already provides workload identity for this exact topology, and it's what Tailscale's own internal tools — TailSQL, Setec, Golink — use for the same purpose). This is necessary, not optional: today's shared Connector/egress proxy pods collapse every worker in a cluster into one indistinguishable caller from SEAM's point of view.
2. **Authorization data:** scope claims (`service:action`, e.g. `k8s-ro:get`, `argocd:sync`) travel in the Tailscale Grant's opaque `app` capability field, read via `WhoIs` on the inbound connection — no separate JWT-issuing auth server needed for tailnet-native callers. Fall back to a bearer-claim-in-header model only for callers that genuinely can't be first-class tailnet peers.
3. **Enforcement:** SEAM tags every route with its required scope(s) and checks the caller's Grant-supplied scopes against them, default-deny, at the gateway — never relying on the calling agent's own tool list (the industry consensus, backed by OWASP's Agentic Top 10 and a live AutoGen cross-agent-tool-leak issue, is that client-side restriction is UX, not a security boundary).
4. **Gap to close first:** NEEDLE mints no identity/credential for spawned agents today. Before any of the above can work, NEEDLE needs new code in `run_process` (`~/NEEDLE/src/dispatch/mod.rs:703`) to provision a per-worker `tsnet` identity and inject it via the existing `adapter.environment` passthrough (or a new template variable) — this is real, scoped implementation work, not configuration.

See `docs/plan/plan.md` for how this folds into SEAM's phased build-out.

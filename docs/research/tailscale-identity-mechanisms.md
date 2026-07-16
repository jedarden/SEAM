# Tailscale-Native Identity Mechanisms for Per-Agent Access Control

Research conducted 2026-07-16. The entire hosting environment is already Tailscale-native (every cluster reachable only via Tailscale, MagicDNS hostnames, Tailscale Kubernetes operator). This asks whether Tailscale's own identity/policy primitives can supply per-agent identity for SEAM instead of a custom auth layer.

## 1. ACLs / Grants — fine-grained rules and app capabilities

Grants (GA, superseding raw ACL `acls` blocks) express `src`/`dst`/`ip`/`app` rules — e.g., `"src": ["tag:worker-x"], "dst": ["tag:seam"], "app": {...}` — down to specific tagged devices and destination:port. Critically, the `app` field carries **opaque, application-defined JSON** (e.g. `"tailscale.com/cap/secrets": [{"action":["get"],"secret":["prod/app/*"]}]`) that Tailscale's policy engine validates only as JSON — it does not enforce or interpret the content. The destination app reads these capabilities off the connection (via `tailscale whois`, `LocalClient.WhoIs`, or the `tsnet` API) and enforces them itself.

This is exactly the shape needed: Tailscale acts as the identity/policy-distribution layer, SEAM would be its own PDP/PEP reading capability claims from Grants — **potentially carrying the `service:action` scope claims from `agent-tool-access-control.md` directly in the Grant's `app` field, with no separate JWT-issuing auth server needed.**

## 2. Node/device identity extraction

`tailscale.com/client/local` (successor to the deprecated `client/tailscale` alias) exposes `WhoIs(remoteAddr)`, resolving an inbound connection's source IP to a stable node + user/tag identity. This works cleanly for **native tailnet peers** — each node keeps a unique 100.x overlay IP end-to-end, even over DERP relay, so no NAT ambiguity exists between two direct tailnet peers.

`tsnet` lets you embed independent Tailscale nodes directly in a Go binary; you can run **one tsnet node per worker/agent** (own hostname, dir, ephemeral auth key, `AdvertiseTags`), giving each its own verifiable identity without a separate daemon.

`tailscale serve`/Funnel also inject `Tailscale-User-Login`/`-User-Name` headers, but only for Serve-proxied paths, not Funnel (public) traffic, and headers are stripped/re-injected if present on inbound requests (anti-spoofing).

## 3. SPIFFE/SPIRE comparison

SPIRE issues short-lived X.509-SVIDs via its own Server/Agent/attestation-plugin infrastructure for workload mTLS — powerful but a genuinely separate control plane (attestation, trust bundle distribution, rotation, revocation) to build and run. Given the environment is already 100% Tailscale-native (WireGuard identity, MagicDNS, existing Grants tooling), layering SPIFFE/SPIRE on top would duplicate an identity/attestation system Tailscale already provides for this exact topology. SPIRE earns its keep when workloads must present identity to non-Tailscale-aware systems or span multiple trust domains/clouds — not the case here.

## 4. Precedent

Not hypothetical — Tailscale's own internal tools are the reference implementations: **TailSQL** (per-user, per-data-source query authz via grants), **Setec** (tsnet-based secrets server gating secret access via grants), and **Golink** (shortlink service using Tailscale identity + grants instead of its own user/role DB). All three are direct architectural analogs to SEAM: an internal tsnet-based gateway using WhoIs + app-capability grants as its entire authz layer.

## Recommendation and critical limitation

Use Tailscale Grants + WhoIs as SEAM's **primary** mechanism, but only for callers that are (or can be made) first-class tailnet peers — this is the load-bearing caveat.

**The real risk is architectural:** this environment's clusters reach each other via the Tailscale Kubernetes operator's Connector/egress ProxyGroups, which DNAT pod-originated traffic through one shared proxy pod holding a single node identity. From SEAM's side, `WhoIs` on such a connection resolves to *the egress proxy*, not the originating pod — collapsing every worker in a cluster into one indistinguishable caller. This directly undermines the "which specific agent" requirement if workers call SEAM through the cluster's existing shared egress path.

**The fix is architectural, not custom-auth:** give each NEEDLE worker/agent its own **tsnet-embedded identity** (ephemeral node, tagged e.g. `tag:needle-worker`) so it calls SEAM directly as a distinct tailnet peer rather than riding the shared Connector egress. At the ~20-worker scale already in use, per-worker ephemeral tsnet nodes are cheap and operationally trivial (no separate token minting/rotation service). For any caller that genuinely can't be a first-class peer (large-fanout ephemeral pods where per-pod nodes would be unreasonable), fall back to a thin bearer-claim riding inside the Tailscale connection from a distinguishable upstream identity (e.g., the NEEDLE dispatcher) rather than a full custom auth stack.

**Net: no separate SPIFFE/SPIRE or homegrown token system is needed** — but SEAM's rollout should explicitly require per-worker tsnet identity as a prerequisite, not just "callers are somewhere on the tailnet." See `docs/research/needle-identity-model.md` for why this requires new NEEDLE-side work (NEEDLE currently mints no identity/credential for spawned agents at all).

### Sources
- tailscale.com/docs/features/access-control/grants
- tailscale.com/docs/features/access-control/grants/grants-app-capabilities
- tailscale.com/docs/reference/examples/grants
- tailscale.com/blog/app-capabilities
- tailscale.com/blog/acl-grants
- pkg.go.dev/tailscale.com/client/local
- pkg.go.dev/tailscale.com/tsnet
- tailscale.com/kb/1244/tsnet
- tailscale.com/docs/kubernetes-operator/egress
- spiffe.io/docs/latest/spire-about/use-cases

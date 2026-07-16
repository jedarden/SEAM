# Prior Art: Secret-Injecting Reverse Proxies / Credential Brokers

Research conducted 2026-07-16 to inform SEAM's core proxy+inject function — surveying existing products that solve the same problem before designing from scratch.

## 1. Vault Agent + Agent Injector (HashiCorp)

Vault Agent runs as a sidecar/init container that authenticates to Vault (via a Kubernetes auth method), fetches secrets, and renders them to a shared `emptyDir` (`/vault/secrets`) using Consul Template — the app container reads a file, never talks to Vault directly. The Injector is a mutating admission webhook: pods get a `vault.hashicorp.com/agent-inject: true` annotation and the webhook rewrites the pod spec on the fly, so no manifest hand-editing is needed per-app.

**Steal:** the "target app is Vault-unaware, secrets arrive as rendered files" decoupling — the gateway's outbound-handler code doesn't need an OpenBao SDK per backend, just a templated value.
**Avoid:** file-based delivery adds staleness/rotation lag; a live HTTP-injecting proxy (SEAM's model) can be tighter since it can fetch-fresh per request.

## 2. HashiCorp Boundary

Boundary separates "credential brokering" (secret handed to the user's client) from "credential injection" (secret handed only to the Boundary *worker*, which authenticates to the target on the user's behalf — the human/agent never sees it). Flow: client → controller (auth + policy) → controller fetches from a credential store (static, or Vault for dynamic/short-lived creds) → controller assigns a worker → worker injects credential and connects to target.

**Steal:** the explicit brokering-vs-injection vocabulary and the controller/worker split — SEAM is architecturally an "injection-only" broker, worth stating explicitly to head off requests for "just give me the token."

## 3. Teleport

Rather than distributing static secrets at all, Teleport's Auth Service acts as a certificate authority issuing short-lived mTLS/SSH certs bound to identity (backed by an external IdP), and the Proxy Service enforces policy per-session with full audit. Nothing resembling a long-lived password is ever near the client.

**Steal:** identity-bound, short-TTL certs instead of static API keys wherever the backend supports mTLS/cert auth — reduces blast radius if the gateway itself is compromised.
**Avoid:** over-scoping SEAM to become a full PKI/CA — that's a much bigger system than a config-driven header injector needs.

## 4. oauth2-proxy

A reverse proxy in front of an app that offloads OIDC login, then forwards `Authorization`/`X-Auth-Request-*` headers to the upstream. Relevant less as a credential-broker and more as a cautionary tale: CVE-2025-64484 showed that if the proxy validates-but-doesn't-strip attacker-supplied header variants, and the backend normalizes header names differently, the attacker's forged header can smuggle through.

**Avoid (critical):** SEAM must strip *all* inbound headers it intends to set itself (especially anything shaped like the injected credential header) before the injection step — never trust-then-overlay.

## 5. Kong / Tyk / KrakenD

Kong's Request Transformer (Advanced) plugin supports `{vault://path}` references inside header-add config, resolved per-route from Vault/AWS Secrets Manager at request time — the closest existing pattern to SEAM's "inject this secret into this outbound header per route" ask. Tyk supports `vault://KEY` refs in its KV config layer; KrakenD does client-credentials OAuth2 exchange per backend (fetch token, cache, attach `Authorization` header).

**Steal:** Kong's declarative `vault://` reference syntax directly in route config is a clean model for SEAM's per-route OpenBao path mapping — maps directly onto the `x-vault-path` extension field already planned for SEAM's OpenAPI route fragments.

## 6. Lightweight OSS secret-injection proxies (closest analogs)

CyberArk **Secretless Broker** is the original "app talks to localhost, broker fetches secret from vault + does the auth handshake to the real target" pattern — client never has the credential in-process. **Infisical Agent Vault** is essentially SEAM's twin, built specifically for AI agents: a MITM TLS-terminating forward proxy where agents are handed dummy placeholder tokens (`__github_pat__`), and per-host rules (bearer/basic/API-key/custom template with `{{CREDENTIAL}}`) declare how the real secret gets substituted before re-establishing TLS outbound. **Envoy Gateway** also ships a native `credential-injection` extension at the gateway layer.

**Steal:** Agent Vault's placeholder-credential UX and declarative per-host template syntax are directly reusable for SEAM's OpenAPI-fragment-driven route config.

## Recommended patterns to adopt

- Strict inbound header stripping before injection (learn from CVE-2025-64484).
- Declarative `vault://`/OpenBao-path reference syntax per route (Kong-style), not code per backend.
- Explicit "injection-only" framing — the agent's client never receives the real secret, only a placeholder/opaque token.
- Fetch-fresh-per-request or short-TTL cache rather than long-lived file rendering, to keep OpenBao rotations effective immediately.
- Schema-validated error responses tied to the same OpenAPI fragments driving `/docs` (SEAM's own idea — no prior art does this well; it's a genuine differentiator worth keeping).

## Pitfalls to avoid

- Don't let the gateway become a general PKI/CA (Teleport-scope creep) when it only needs header/token injection.
- Don't render secrets to shared files/volumes if you can inject live — reduces on-disk exposure surface, important in a shared K8s namespace.
- Don't trust any inbound header that collides with an injected one; normalize and strip first.
- Don't couple secret-fetch tightly to one storage backend's SDK — keep an abstraction layer since OpenBao/Vault paths may later need additional stores.

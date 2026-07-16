# Per-Agent Tool Access Control — State of the Art (2026)

Research conducted 2026-07-16 into how the industry scopes which tools/routes an individual LLM agent instance can access — informing SEAM's near-future per-agent tool scoping requirement.

## 1. MCP Gateways — real and converging, but not protocol-native

A genuine "MCP gateway" category matured through 2025–2026: Kong AI Gateway (v3.13, Jan 2026) does per-consumer-group tool ACLs with default-deny and dynamic tool-list filtering; Envoy AI Gateway (`MCPRoute` + JWT-claim authorization), Traefik Hub ("Task/Tool-Based Access Control"), Solo.io kgateway, Portkey, and WSO2 all shipped MCP-specific per-agent tool scoping within roughly a 12-month window. On the OSS side, IBM's mcp-context-forge (4.1k stars, RBAC scopes) and Docker's MCP Gateway/Toolkit (per-profile allowlists) are the strongest non-vendor examples; AWS Bedrock AgentCore Gateway (GA) is the clearest managed-cloud one.

Critically, this scoping is bolted on top of MCP, not built into it. The current MCP authorization spec: authorization is explicitly **optional**, modeled as OAuth 2.1 resource-server auth, and scopes are server/resource-level only — there is no `scopes` field on the `Tool` schema. The proposal to add per-tool scopes (SEP-1880) was formally **closed "not planned"**; its successor (SEP-1488/1881) remains an open, stalled draft.

**Verdict: gateway-enforced per-agent tool scoping is established practice; protocol-native tool-level scoping is not, and isn't imminent.** This validates SEAM's approach of enforcing scoping at the gateway rather than waiting on MCP itself to grow this feature.

## 2. Agent IAM — coarse scoping is GA; true per-tool scoping is narrow

Vendors shipped faster than expected: Auth0 for AI Agents (GA Nov 2025, Token Vault/RFC 8693 token exchange), Microsoft Entra Agent ID (GA ~Apr 2026, per-audience tokens + Conditional Access — the most fine-grained shipped mechanism found), WorkOS (FGA + Machine Identity, GA), Descope Agentic Identity Hub 2.0 (claims per-tool MCP scopes, GA Jan 2026). Okta's own tool-call-authorization piece is still "coming soon" despite GA branding around it.

**Connection/audience-level scoping is genuinely established; true per-tool-call authorization is shipped only narrowly** (Claude Code's own `settings.json` permission rules are one real example). Treat "Agent IAM" as a fast-consolidating vendor category, not a converged standard.

## 3. Multi-agent frameworks — client-side by default, but the security consensus wants server-side

LangGraph, CrewAI, AutoGen/AG2, OpenAI's Agents SDK, and Semantic Kernel all default to **context-based** restriction: a sub-agent simply isn't handed a tool's definition. That's a UX convenience, not a security boundary — AutoGen has an open issue (#4399) of agents invoking tools registered only to *other* agents in the same session. All five frameworks have since bolted on interception hooks (LangChain middleware, CrewAI `before_tool_call`, OpenAI Guardrails) that run outside the model's context.

OWASP's Excessive Agency / Agentic Top 10 (2025–2026) guidance and multiple gateway vendors converge on identical reasoning: prompt injection can make a model *attempt* a forbidden call, but cannot make an independent check *approve* it — enforcement must sit outside the model's reasoning loop.

**This is the most settled finding of the four research areas: never rely on the calling agent's own tool list as a security boundary.** SEAM must enforce scoping server-side regardless of what the agent's harness thinks it's allowed to call.

## 4. OAuth2 scopes/JWT as the reuse target

No credible push to invent a new DSL. MCP, Google's A2A (now Linux Foundation, v1.0 stable), AGNTCY, and IETF's identity-chaining draft all delegate to OAuth 2.1/OIDC/Token Exchange (RFC 8693) rather than a bespoke scheme. Practitioner convention (Auth0, Descope, GitHub's MCP server) is "one scope per tool" — a convention layered on OAuth, not a protocol requirement. A complementary layer (OpenID's AuthZEN/COAZ) is emerging specifically because plain scopes can't express "may this agent call this tool with these arguments" — still an early draft, not ratified.

## Recommendation

Don't invent a scoping DSL. Issue each agent identity (per Claude Code session, per NEEDLE worker role) a scope claim using the industry-converged `service:action` convention (e.g. `k8s-ro:get`, `argocd:sync`). Tag every SEAM route with the scope(s) it requires. The gateway checks the caller's scope against the route's declared tag before proxying, **default-deny** — mirroring exactly what Kong/Envoy/Traefik ship for MCP today, and never relying on the calling agent's own tool list (per §3).

If SEAM later needs argument-level control ("k8s access but only namespace X"), track AuthZEN/COAZ — but it's not ratified, so don't design around it yet.

See `docs/research/tailscale-identity-mechanisms.md` for how the scope claim itself might be carried (Tailscale Grants' `app` capability field vs. a separate JWT-issuing auth server) — the two research threads combine into a single recommendation in `docs/plan/plan.md`.

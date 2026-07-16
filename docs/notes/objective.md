# Objective

A single unified endpoint that acts as a proxy to multiple authenticated services. It injects the secret into the request received from the agent, sends the request on to the destination, and passes the response back. The agent never sees the secret.

This is the entire core function of SEAM. Everything else — OpenAPI-driven self-documentation, ConfigMap-mounted route fragments, per-cluster reachability — exists in service of that one objective.

## Near-future requirement: per-agent tool scoping

Today, any caller that can reach SEAM can call any route SEAM exposes. That won't hold going forward: it will become important to configure specifically which tools/routes a given agent has access to, rather than exposing every route to every agent unconditionally.

This will likely require some form of identity handshake between SEAM and NEEDLE (the agent worker fleet) — SEAM needs to know *which* agent/worker is calling before it can decide which routes that caller is allowed to reach and which secrets it's allowed to have injected on its behalf. See `docs/research/` for the investigation into how this could work, and `docs/plan/plan.md` for how it fits into the phased build-out.

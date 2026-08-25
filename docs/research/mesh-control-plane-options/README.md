# Mesh control-plane options for SEAM

**Status:** research, not an architecture decision  
**Recorded:** 2026-08-25

## Executive conclusion

SEAM does not need a Tailscale or Kubernetes-operator upgrade to enforce its
Phase 6a listener boundary. The deployed Tailscale Kubernetes operator v1.94.2
already supports the `tailscale.com/tags` annotation. The immediate failure is
that the authoritative tailnet policy does not define or delegate the planned
`tag:seam` tag, and the policy is not currently managed from Git.

The lowest-risk next step is to make the existing Tailscale policy
declarative. Migrating the coordination plane is justified only if
self-hosting, control-plane sovereignty, or provider independence is itself a
goal. It is unnecessary merely to obtain policy as code.

## Live finding: why Phase 6a is blocked

The SEAM Tailscale proxy was enrolled as `tag:k8s`. That generic destination
tag is covered by broader cluster-access policy and cannot express SEAM's
required boundary:

- `tag:needle-worker`, `tag:devpod`, and operators may reach caller port 8080.
- Only the explicit operator source set may reach operator port 8081.

SEAM already carries the intended policy fragment in
[`docs/tailnet-policy.hujson`](../../tailnet-policy.hujson). During live
verification, adding `tailscale.com/tags: "tag:seam"` and re-enrolling the
proxy through GitOps reached the Tailscale API but failed with:

```text
requested tags [tag:seam] are invalid or not permitted
```

The proxy was restored under `tag:k8s`; no direct Kubernetes mutation was
used. Bead `seam-6a4c6d91` tracks applying the policy, re-enrolling the proxy,
and verifying access from representative worker and operator identities.

One testing subtlety matters: an untagged device can still belong to
`autogroup:admin`, because that identity is user-based. A successful operator
request from an untagged workstation is therefore not evidence of a policy
failure. Acceptance requires a real `tag:needle-worker` or `tag:devpod` source
to succeed on 8080 and be refused on 8081, plus a positive operator test on
8081.

## Declarative Tailscale without migration

Tailscale exposes its policy through an API and a Terraform/OpenTofu provider.
A suitable operating model is:

1. Store the complete authoritative policy in `declarative-config`.
2. Validate its embedded policy tests before deployment.
3. Apply it through a reviewed OpenTofu plan or an Argo Workflow calling the
   policy API.
4. Keep the API credential in OpenBao and pass it directly to the client
   without exposing the value in arguments or logs.
5. Compare the committed and live policy hashes to detect drift.
6. After the policy accepts `tag:seam`, add the annotation to
   `k8s/rs-manager/seam/service.yaml` and let ArgoCD and the Tailscale operator
   re-enroll the proxy.

References:

- [Tailscale policy-file API](https://tailscale.com/api#tag/policy-file)
- [Tailscale Terraform provider ACL resource](https://registry.terraform.io/providers/tailscale/tailscale/latest/docs/resources/acl)
- [Tailscale access-control syntax](https://tailscale.com/kb/1337/acl-syntax)

## Headscale assessment

Headscale is a self-hosted coordination server for Tailscale clients. It is a
reasonable candidate when the objective is control-plane sovereignty,
offline/provider-independent operation, or avoiding a hosted coordination
dependency. It is not the shortest fix for SEAM's policy gap.

Migration would add responsibility for:

- the public HTTPS coordination endpoint and DNS;
- database durability, backup, restoration, and upgrades;
- OIDC and user lifecycle integration;
- policy compatibility and rollout;
- monitoring and incident response;
- DERP placement or reliance on an external DERP map; and
- compatibility testing for the Tailscale Kubernetes operator, ephemeral
  NEEDLE workers, routes, tags, DNS, and future SEAM identity behavior.

The Kubernetes operator uses control-plane APIs in addition to the client
wire protocol. Retaining Tailscale clients does not by itself prove that every
operator workflow or managed-control-plane feature will behave identically
against Headscale.

References:

- [Headscale documentation](https://headscale.net/)
- [Headscale policy documentation](https://headscale.net/stable/ref/acls/)
- [Headscale installation](https://headscale.net/stable/setup/install/official/)
- [Headscale DERP configuration](https://headscale.net/stable/ref/derp/)

## Hosting Headscale on Oracle Free Tier

Headscale's compute needs are small enough for an Oracle Ampere A1 free-tier
instance. That makes the platform useful for a lab, compatibility pilot, or
disaster-recovery exercise, but it is a weak sole home for a production
coordination plane:

- idle Always Free compute may be reclaimed under Oracle's published rules;
- exhausted free-tier capacity can delay recreation after loss;
- an account or provider incident affects enrollment, policy changes,
  node-map updates, and key lifecycle;
- the boot volume must not be the only database copy; and
- recovery has not been demonstrated until the service can be restored on a
  different provider.

A sensible pilot uses stable public DNS and TLS, daily off-provider backups,
external uptime monitoring, disposable clients, and a documented restore onto
a second provider. It should remain non-authoritative until SEAM, Kubernetes,
ephemeral workers, ACL changes, revocation, DNS, routes, and restoration all
pass.

References:

- [Oracle Cloud Free Tier](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier.htm)
- [Headscale installation](https://headscale.net/stable/setup/install/official/)

## Alternatives

| System | Best fit | Principal tradeoff |
|---|---|---|
| [Headscale](https://headscale.net/) | Preserve Tailscale clients while self-hosting coordination | Smaller/different feature surface; operator compatibility must be proved |
| [NetBird](https://docs.netbird.io/) | Full-featured self-hosted mesh with identity, routes, DNS, relays, API, and management UI | More components to operate |
| [Netmaker](https://docs.netmaker.io/) | Site-to-site and Kubernetes-oriented WireGuard networking | More network-centric and less agent-identity-centric |
| [ZeroTier](https://docs.zerotier.com/) | Mature virtual Layer 2/Layer 3 networking | Different data plane and policy model |
| [Nebula](https://nebula.defined.net/docs/) | Small, auditable infrastructure mesh under static PKI | Certificate and firewall-policy distribution become operator duties |
| [Firezone](https://www.firezone.dev/kb) | Identity-aware access to named private resources | ZTNA/resource access rather than a general peer mesh |
| [WireGuard](https://www.wireguard.com/) | Small, stable site-to-site topology | No discovery, identity plane, policy service, or automated key lifecycle |
| [Cloudflare Zero Trust](https://developers.cloudflare.com/cloudflare-one/) | Browser/application access and private resource publishing | Not a direct arbitrary peer-to-peer mesh replacement |

For SEAM, NetBird is the strongest clean-break candidate, Headscale is the
lowest-friction client migration candidate, Nebula is the strongest minimalist
candidate, and Netmaker is most attractive if cluster-to-cluster networking
dominates per-agent authorization.

## Recommended evaluation sequence

1. Put the current Tailscale policy under Git and complete Phase 6a.
2. Pilot Headscale and NetBird in parallel with disposable identities; do not
   move production nodes yet.
3. Test enrollment, revocation, per-port policy, Kubernetes exposure,
   ephemeral workers, subnet routing, DNS, relay behavior, upgrades, backup,
   and cross-provider restoration.
4. Record measured operational effort and compatibility gaps.
5. Migrate only if control-plane ownership provides enough value to justify
   operating the additional critical service.


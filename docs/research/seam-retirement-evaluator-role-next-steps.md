# seam-retirement-evaluator OpenBao Role: Next Steps

**Decision date:** 2026-08-16
**Related beads:** `seam-b9d1656f`, `seam-2496976a`, `seam-d70c5345`, `seam-7b5ee880`

## Decision

The `seam-retirement-evaluator` role needs to be **created in the target OpenBao instance**. It should not be treated as an existing role that merely needs an update.

The repository and declarative-config files describe the intended role, but they do not prove that the role exists in OpenBao. No live role read-back was available from the investigation, so there is no evidence-based role update to make at this time. After provisioning, compare the live role with the expected configuration below and update it only if the read-back shows drift.

## Findings from the investigation

| Area | Finding | Implication |
| --- | --- | --- |
| Kubernetes auth role listing | `kubernetes-auth-roles-list.txt` records that the queried OpenBao server had only `token/` enabled; Kubernetes auth and its role list were absent. | `seam-retirement-evaluator` was not found in that server. |
| Runtime verification | `openbao-resource-verification-report.md` records that OpenBao was healthy, but the evaluator ServiceAccount was absent from the checked cluster and the role/policy could not be verified without admin access. | Runtime provisioning remains incomplete or unproven. |
| Desired Kubernetes identity | The authoritative declarative-config ServiceAccount is `seam-retirement-evaluator` in namespace `seam`. | The OpenBao binding must use the exact name and namespace. |
| Desired role definition | The authoritative workflow writes `auth/kubernetes/role/seam-retirement-evaluator` with policy `seam-retirement-evaluator-policy`, a 24-hour TTL, and a 72-hour maximum TTL. | The role specification is already defined; the missing step is applying it to OpenBao. |
| Provisioning mechanism | `setup-openbao-resources.yml` documents that the role is created by the Argo WorkflowTemplate, not by an `OpenBaoKubernetesAuthRole` Kubernetes CRD. | Run the supported workflow after its Kubernetes and OpenBao prerequisites are ready. |

## Expected role configuration

- Role: `seam-retirement-evaluator`
- Auth mount: `auth/kubernetes`
- Bound ServiceAccount: `seam-retirement-evaluator`
- Bound namespace: `seam`
- Policy and default policy: `seam-retirement-evaluator-policy`
- Token TTL: `24h`
- Token max TTL: `72h`

The attached policy must remain least-privilege: read the evaluator's own credential path and the required VictoriaMetrics path, explicitly deny `secret/data/seam/routes/*`, and deny unrelated secret paths.

## Configuration gaps to resolve

1. **Runtime resources are not established.** Ensure the authoritative GitOps manifests are reconciled and that the `seam-retirement-evaluator` ServiceAccount exists in namespace `seam` before attempting Kubernetes-auth login.
2. **Kubernetes auth is not proven enabled or configured on the target OpenBao instance.** An operator with OpenBao administration access must enable/configure that mount if necessary before the role can be created.
3. **The role and policy are not proven provisioned.** Execute the `seam-retirement-evaluator-openbao-setup` WorkflowTemplate, then read back the role and policy through an approved administrative path.
4. **Secret-path conventions are inconsistent.** The current `iad-ci` setup and verify WorkflowTemplates use `secret/data/evaluators/seam-retirement-evaluator/*`, while the `k8s/rs-manager/seam-retirement-evaluator` policy, README, and helper scripts use `secret/data/seam-retirement-evaluator/*`. Select one canonical path and update the policy, workflow, verification, and documentation together before provisioning. The latest setup workflow is the strongest current indication that `evaluators/seam-retirement-evaluator/*` is intended, but this requires an explicit source-of-truth decision.
5. **The SEAM staging copy is stale.** Files under `declarative-config/infra/seam-retirement-evaluator/` still describe the ServiceAccount as being in a dedicated `seam-retirement-evaluator` namespace while the authoritative declarative-config uses namespace `seam`. Do not change the live role to match this stale copy.

## Recommended next sequence

1. Reconcile the authoritative namespace and ServiceAccount manifests through GitOps.
2. Resolve the credential-path convention and make all policy/workflow/verification references consistent.
3. Have an authorized operator enable/configure Kubernetes auth on the target OpenBao instance and run the provisioning WorkflowTemplate using the approved secret-delivery mechanism. Do not place credential values in this repository, logs, or task notes.
4. Read back `auth/kubernetes/role/seam-retirement-evaluator` and `seam-retirement-evaluator-policy`; confirm the binding, policies, TTLs, and path capabilities match the approved configuration.
5. Run the verification workflow from the evaluator identity. Confirm authentication, access to the evaluator's own path and VictoriaMetrics, and denial of SEAM route and unrelated secret paths without recording secret contents.

Until those checks pass, the evaluator's OpenBao prerequisite should remain marked **not provisioned**. The next implementation action is role and policy creation, followed by verification; it is not an update to an already confirmed role.

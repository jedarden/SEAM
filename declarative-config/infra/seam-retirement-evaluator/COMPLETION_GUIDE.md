# seam-retirement-evaluator Provisioning - Completion Guide

## Status: Infrastructure Defined ✅ | Manual Steps Required 🔧

### What Has Been Completed

✅ **Kubernetes infrastructure committed to declarative-config**
- Namespace `seam` defined and committed to `k8s/rs-manager/seam-retirement-evaluator/namespace.yaml`
- ServiceAccount `seam-retirement-evaluator` defined and committed to `k8s/rs-manager/seam-retirement-evaluator/serviceaccount.yaml`
- Changes pushed to Forgejo origin: `git.ardenone.com/jedarden/declarative-config.git`
- Commit: `c880216e` - "feat(seam): add seam-retirement-evaluator infrastructure"

✅ **OpenBao policy and configuration defined**
- OpenBao HCL policy (`openbao-policy.hcl`) committed with proper isolation
- Kubernetes auth role specification (`openbao-auth.yml`) committed
- Provision scripts (`provision-openbao.sh`) committed
- Verification scripts (`verify-openbao-setup.sh`) committed

✅ **Comprehensive documentation created**
- README.md with architecture, security model, and setup instructions
- GitHub token setup guide (`github-token-setup.sh`)
- Verification procedures documented

### Manual Steps Required

The following steps require manual execution with appropriate credentials:

#### 1. Apply Kubernetes Resources (Immediate)

The Kubernetes manifests have been committed to declarative-config but need to be applied. Options:

**Option A: Via ArgoCD (Recommended)**
- Create ArgoCD Application to sync `k8s/rs-manager/seam-retirement-evaluator/` 
- Let ArgoCD automatically deploy namespace and ServiceAccount

**Option B: Manual kubectl apply (Immediate workaround)**
```bash
kubectl apply -f k8s/rs-manager/seam-retirement-evaluator/namespace.yaml
kubectl apply -f k8s/rs-manager/seam-retirement-evaluator/serviceaccount.yaml
```

#### 2. Provision OpenBao Resources (Admin Access Required)

Run the provision script with OpenBao admin credentials:

```bash
cd k8s/rs-manager/seam-retirement-evaluator
export BAO_ADDR="http://openbao-rs-manager.openbao.svc.cluster.local:8200"
export BAO_TOKEN="<your-admin-token>"  # Requires OpenBao admin/root token
./provision-openbao.sh
```

This creates:
- OpenBao policy `seam-retirement-evaluator-policy`
- Kubernetes auth role `seam-retirement-evaluator`
- Token TTL: 24h, Max TTL: 72h

#### 3. Create GitHub Token (Manual)

1. Visit GitHub Settings → Developer settings → Personal access tokens → Tokens (classic)
2. Generate new token (classic) with:
   - Note: "seam-retirement-evaluator PR token"
   - Expiration: 90 days
   - Scope: `repo` (Full control of private repositories)
3. Copy token immediately

#### 4. Store GitHub Token in OpenBao (Admin Access Required)

```bash
bao kv put secret/seam-retirement-evaluator/github-token \
  token=ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

**Security verification**: Token stored at `secret/data/evaluators/seam-retirement-evaluator/*` (outside `seam/routes/*` hierarchy)

#### 5. Configure VictoriaMetrics Access (If Needed)

Ensure VictoriaMetrics credentials exist at:
```
secret/data/monitoring/victoriametrics/*
```

The evaluator's policy already grants read access to this path.

#### 6. Verify Security Isolation

From a pod using the `seam-retirement-evaluator` ServiceAccount, run:

```bash
./verify-openbao-setup.sh
```

**Expected results:**
- ✅ Can read own GitHub token (`secret/data/evaluators/seam-retirement-evaluator/github-token`)
- ✅ Cannot read SEAM routes (`secret/data/seam/routes/*`) - permission denied
- ✅ Can read VictoriaMetrics credentials (`secret/data/monitoring/victoriametrics/*`)

### Verification Checklist

Before considering this precondition complete, verify:

- [ ] Namespace `seam` exists in cluster
- [ ] ServiceAccount `seam-retirement-evaluator` exists in namespace `seam`
- [ ] OpenBao policy `seam-retirement-evaluator-policy` exists
- [ ] OpenBao Kubernetes auth role `seam-retirement-evaluator` exists
- [ ] GitHub token stored at `secret/data/evaluators/seam-retirement-evaluator/github-token`
- [ ] VictoriaMetrics credentials accessible at `secret/data/monitoring/victoriametrics/*`
- [ ] Evaluator can read own GitHub token
- [ ] Evaluator cannot read SEAM route secrets
- [ ] SEAM cannot read evaluator's GitHub token

### Next Steps After Provisioning

Once the manual steps are completed:

1. **Deploy evaluator workload**: Create the evaluator Deployment using the `seam-retirement-evaluator` ServiceAccount
2. **Test metrics access**: Run the VictoriaMetrics query test to verify the evaluator can query SEAM metrics:
   ```bash
   kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig create -f - <<EOF
   apiVersion: argoproj.io/v1alpha1
   kind: Workflow
   metadata:
     generateName: seam-retirement-evaluator-vm-query-
     namespace: argo-workflows
   spec:
     workflowTemplateRef:
       name: evaluator-victoriametrics-query-test
   EOF
   ```
3. **Test PR creation**: Verify the evaluator can open PRs against declarative-config
4. **Monitor security**: Regular verification of isolation policies

### Files in declarative-config

All infrastructure files are now in:
```
k8s/rs-manager/seam-retirement-evaluator/
├── README.md                          # Comprehensive documentation
├── namespace.yaml                     # Kubernetes namespace
├── serviceaccount.yaml                # ServiceAccount for OpenBao auth
├── openbao-policy.hcl                 # OpenBao access policy
├── openbao-auth.yml                   # Kubernetes auth role spec
├── provision-openbao.sh               # Automated OpenBao setup
├── verify-openbao-setup.sh            # Security verification
├── github-token-setup.sh              # GitHub token guide
└── setup-openbao-resources.yml        # Additional setup resources
```

### Security Model Verification

The implemented security model ensures:

1. **Evaluator identity isolation**: Separate ServiceAccount and OpenBao role
2. **Credential isolation**: GitHub token stored outside `seam/routes/*` hierarchy  
3. **Access control**: Evaluator policy explicitly denies `seam/routes/*` access
4. **Reverse isolation**: SEAM policy explicitly denies `seam-retirement-evaluator/*` access
5. **Token scope limitation**: GitHub token only has `repo` scope (not full org admin)

### Troubleshooting

**Namespace not created**: Check if ArgoCD is syncing the path or apply manually with kubectl

**OpenBao authentication fails**: Verify ServiceAccount exists and OpenBao role is properly configured

**Token cannot be read**: Ensure token is stored at correct path (`secret/data/evaluators/seam-retirement-evaluator/*`, not `secret/data/seam/routes/*`)

**GitHub token invalid**: Verify token has `repo` scope and hasn't expired

### References

- OpenBao setup documentation: `docs/notes/openbao-seam-setup.md`
- SEAM policy for comparison: `declarative-config/infra/seam/seam-openbao-policy.hcl`
- ArgoCD applications: Check ArgoCD UI for sync status

---

**Last updated**: 2026-08-09  
**Status**: Infrastructure defined and committed, manual provisioning steps required  
**Commit**: c880216e - "feat(seam): add seam-retirement-evaluator infrastructure"
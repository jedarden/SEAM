# OpenBao Argo Workflow Templates

This document describes the OpenBao write/provisioning workflow templates available in the SEAM project.

## Overview

The following OpenBao workflow templates are available for write and provisioning operations:

1. **openbao-write-test.yaml** - Testing template for write operations
2. **openbao-provision-resources.yaml** - Production template for resource provisioning

Both templates are located in: `declarative-config/k8s/iad-ci/argo-workflows/`

---

## Template 1: openbao-write-test

### Purpose

Test template for verifying OpenBao write operations, secret engine management, and provisioning capabilities. This is designed for testing and validation, not production resource creation.

### What It Tests

1. **Authentication**: Kubernetes service account authentication to OpenBao
2. **Secret Engine Enable**: Optional test engine enablement
3. **Secret Write**: Create a test secret with custom value
4. **Secret Read**: Read back and verify written values
5. **Secret Update**: Modify existing secret values
6. **Metadata Creation**: Create configuration/metadata secrets
7. **List Operations**: Attempt to list secrets (if permitted by policy)
8. **Patch Operations**: Test secret patch/update functionality

### Resources Created

During test execution, the following **test resources** are created:

- **Secret**: `secret/data/test/write-test` (configurable via parameter)
  - Contains: value, created_by, created_on, workflow_run
  - Updated during test with: updated_by, updated_on
  - Optionally patched with: patched_by

- **Metadata Secret**: `secret/data/test/write-test-metadata`
  - Contains: config, environment, version, metadata_test

- **Secret Engine** (optional): `test-engine` (configurable via parameter)
  - Type: KV v2
  - Only created if `enable-test-engine=true`

### Parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `test-secret-path` | `secret/data/test/write-test` | Path for test secret |
| `test-secret-value` | `test-value-from-workflow` | Value to write to test secret |
| `enable-test-engine` | `false` | Whether to enable a test secret engine |
| `engine-name` | `test-engine` | Name for test secret engine |

### Usage Example

```bash
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig \
  create -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: openbao-write-test-
  namespace: argo-workflows
spec:
  workflowTemplateRef:
    name: openbao-write-test
EOF
```

### Custom Parameters Example

```bash
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig \
  create -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: openbao-write-test-custom-
  namespace: argo-workflows
spec:
  workflowTemplateRef:
    name: openbao-write-test
  arguments:
    parameters:
    - name: test-secret-path
      value: "secret/data/test/custom-write-test"
    - name: test-secret-value
      value: "my-custom-test-value"
    - name: enable-test-engine
      value: "true"
    - name: engine-name
      value: "my-test-engine"
EOF
```

### Cleanup Required

After running tests, manually clean up test resources:

```bash
# Delete test secrets
bao kv delete secret/test/write-test
bao kv delete secret/test/write-test-metadata

# Disable test engine (if enabled)
bao secrets disable test-engine
```

### Authentication Requirements

- Uses Kubernetes auth method with role: `argo-workflow`
- ServiceAccount: `argo-workflow` (in `argo-workflows` namespace)
- Required OpenBao permissions:
  - `create`, `update`, `read` on `secret/data/test/*`
  - `create`, `delete` on `sys/mounts` (for engine enable)
  - `read` on `auth/kubernetes/login`

---

## Template 2: openbao-provision-resources

### Purpose

Production template for provisioning OpenBao resources. Supports three main operations: creating secrets, enabling secret engines, and creating policies.

### Supported Operations

1. **create-secret**: Create or update a secret with custom data
2. **enable-engine**: Enable a new secret engine (KV v2, etc.)
3. **create-policy**: Create or update an OpenBao policy

### Resources Created

Resources created depend on the operation:

| Operation | Resource Created | Details |
|-----------|-----------------|---------|
| `create-secret` | Secret at specified path | Includes metadata: created_by, created_on, namespace, service_account |
| `enable-engine` | Secret engine at specified path | Type specified by parameter (default: KV v2) |
| `create-policy` | Policy with specified name | HCL format policy rules |

### Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `operation` | Yes | `create-secret` | Operation: `create-secret`, `enable-engine`, `create-policy` |
| `secret-path` | For `create-secret` | `""` | Path where secret will be created |
| `secret-data` | For `create-secret` | `{}` | JSON object with key-value pairs |
| `engine-path` | For `enable-engine` | `""` | Mount path for secret engine |
| `engine-type` | For `enable-engine` | `kv-v2` | Type of secret engine |
| `policy-name` | For `create-policy` | `""` | Name of policy to create |
| `policy-rules` | For `create-policy` | `""` | HCL format policy rules |
| `dry-run` | No | `false` | If true, only log what would be done |

### Usage Examples

#### Example 1: Create a Secret

```bash
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig \
  create -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: openbao-create-secret-
  namespace: argo-workflows
spec:
  workflowTemplateRef:
    name: openbao-provision-resources
  arguments:
    parameters:
    - name: operation
      value: "create-secret"
    - name: secret-path
      value: "secret/data/myapp/config"
    - name: secret-data
      value: '{"database_url":"postgres://localhost/db","api_key":"sk-1234567890"}'
EOF
```

#### Example 2: Enable a Secret Engine

```bash
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig \
  create -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: openbao-enable-engine-
  namespace: argo-workflows
spec:
  workflowTemplateRef:
    name: openbao-provision-resources
  arguments:
    parameters:
    - name: operation
      value: "enable-engine"
    - name: engine-path
      value: "myapp-secrets"
    - name: engine-type
      value: "kv-v2"
EOF
```

#### Example 3: Create a Policy

```bash
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig \
  create -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: openbao-create-policy-
  namespace: argo-workflows
spec:
  workflowTemplateRef:
    name: openbao-provision-resources
  arguments:
    parameters:
    - name: operation
      value: "create-policy"
    - name: policy-name
      value: "myapp-policy"
    - name: policy-rules
      value: |
        path "secret/data/myapp/*" {
          capabilities = ["create", "read", "update", "delete", "list"]
        }
EOF
```

#### Example 4: Dry Run (Test Without Creating)

```bash
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig \
  create -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: openbao-dryrun-
  namespace: argo-workflows
spec:
  workflowTemplateRef:
    name: openbao-provision-resources
  arguments:
    parameters:
    - name: operation
      value: "create-secret"
    - name: secret-path
      value: "secret/data/test/config"
    - name: secret-data
      value: '{"test":"value"}'
    - name: dry-run
      value: "true"
EOF
```

### Error Handling

The workflow includes comprehensive error handling:

- **Connectivity errors**: Validates OpenBao is reachable before attempting operations
- **Authentication errors**: Fails immediately if Kubernetes auth fails
- **Permission errors**: Reports specific permission denied errors
- **Validation errors**: Verifies required parameters are provided for each operation
- **Operation verification**: Reads back created resources to confirm creation

### Authentication Requirements

- Uses Kubernetes auth method with role: `argo-workflow`
- ServiceAccount: `argo-workflow` (in `argo-workflows` namespace)
- Required OpenBao permissions depend on operation:
  - **create-secret**: `create`, `update` on target secret path
  - **enable-engine**: `create`, `delete` on `sys/mounts`
  - **create-policy**: `create`, `update` on `sys/policy`

### Retry Strategy

Both templates use a retry strategy with limit: 2, meaning failed operations will be retried up to 2 times before marking the workflow as failed.

### Monitoring Execution

To monitor workflow execution:

```bash
# List recent workflows
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig \
  get workflows -n argo-workflows --sort-by=.metadata.creationTimestamp | tail -10

# Get workflow status
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig \
  get workflow <workflow-name> -n argo-workflows \
  -o jsonpath='{.status.phase} - {.status.message}'

# Stream logs from running workflow
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig \
  logs -n argo-workflows <pod-name> -c main -f
```

---

## Security Considerations

### Authentication

Both templates use Kubernetes service account authentication, which is the recommended method for workloads within the cluster. The `argo-workflow` ServiceAccount must have a corresponding role in OpenBao.

### Authorization

Ensure the `argo-workflow` role in OpenBao has appropriate permissions:

```hcl
# For write-test template
path "secret/data/test/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "sys/mounts/*" {
  capabilities = ["create", "read", "delete", "list"]
}

# For provisioning template (example)
path "secret/data/*" {
  capabilities = ["create", "update", "read"]
}

path "sys/mounts/*" {
  capabilities = ["create", "delete", "read"]
}

path "sys/policy/*" {
  capabilities = ["create", "update", "read"]
}
```

### Secret Data

- **Never include actual secret values** in workflow definitions or logs
- Use parameterized input for secret values
- Consider using ExternalSecrets or similar mechanisms for production secret injection
- Test secrets should use placeholder values

### Auditing

OpenBao audit logs should be enabled to track all provisioning operations:

```bash
# Check if audit is enabled
bao audit list

# View audit logs (if file audit is enabled)
bao cat /var/log/openbao/audit.log
```

---

## Troubleshooting

### Common Issues

#### 1. Permission Denied Errors

**Symptom**: Workflow fails with "permission denied"

**Solution**: Check that the `argo-workflow` role has appropriate permissions in OpenBao:

```bash
bao read auth/kubernetes/role/argo-workflow
bao policy read argo-workflow-policy
```

#### 2. Authentication Failures

**Symptom**: "Failed to authenticate via Kubernetes auth"

**Solution**: Verify:
- Kubernetes auth method is enabled in OpenBao
- The `argo-workflow` role exists
- The ServiceAccount exists in the correct namespace

```bash
bao auth list | grep kubernetes
bao read auth/kubernetes/role/argo-workflow
kubectl get sa argo-workflow -n argo-workflows
```

#### 3. Connectivity Issues

**Symptom**: "Cannot reach OpenBao at..."

**Solution**:
- Verify OpenBao is running: `kubectl get pods -n openbao`
- Check service: `kubectl get svc openbao -n openbao`
- Test from within cluster: `kubectl run -it --rm debug --image=curlimages/curl -- sh -c "curl http://openbao.openbao.svc.cluster.local:8200/v1/sys/health"`

#### 4. Secret Already Exists

**Symptom**: Secret creation fails when secret already exists

**Solution**: This is expected behavior. Use `bao kv patch` or `bao kv put` to update existing secrets.

#### 5. Engine Already Enabled

**Symptom**: Engine enable fails with "already exists"

**Solution**: The workflow handles this gracefully and logs a warning. No action needed.

---

## Comparison with Read Templates

| Feature | Read Templates | Write/Provisioning Templates |
|---------|----------------|------------------------------|
| **Purpose** | Verify access and connectivity | Create/update resources |
| **Permissions** | Read-only | Write, create, update |
| **Risk Level** | Low (no modifications) | Medium (creates resources) |
| **Use Case** | Testing access, validation | Provisioning, setup |
| **Cleanup** | None required | Manual cleanup needed |
| **Examples** | `openbao-read-test.yaml` | `openbao-write-test.yaml`, `openbao-provision-resources.yaml` |

---

## Best Practices

1. **Use dry-run first**: Always test provisioning with `dry-run=true` before actual execution
2. **Test with write-test**: Use `openbao-write-test` to verify permissions before provisioning
3. **Clean up test resources**: Manually delete test secrets and engines after testing
4. **Monitor execution**: Stream logs during execution to catch errors early
5. **Audit regularly**: Review OpenBao audit logs to track provisioning activities
6. **Limit permissions**: Grant minimum required permissions to the `argo-workflow` role
7. **Version control**: Keep workflow templates in git and review changes
8. **Document resources**: Maintain documentation of provisioned resources

---

## Related Documentation

- [OpenBao Kubernetes Authentication](./setup-openbao-resources.yml) - Role configuration
- [seam-retirement-evaluator-openbao-setup](../openbao-setup-job.yaml) - Example provisioning workflow
- [OpenBao Documentation](https://openbao.org/docs/) - Official OpenBao documentation

---

## Version History

| Version | Date | Changes |
|---------|------|---------|
| 1.0 | 2026-08-14 | Initial write/provisioning templates created |

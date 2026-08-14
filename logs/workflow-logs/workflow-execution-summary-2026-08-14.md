# Workflow Execution Log Capture Summary

**Captured:** 2026-08-14T06:53:42Z  
**Task:** bf-2946c - Capture workflow execution logs and check completion status

## Workflow Details

### Primary Workflow: openbao-read-test-manual-d6zg6
- **Status:** Failed ❌
- **Completion:** 2026-08-14T06:53:42Z (Terminal state reached)
- **Duration:** 20 seconds (06:53:22Z → 06:53:42Z)
- **Exit Code:** 1
- **Error Message:** "No more retries left"

### Execution Attempts
The workflow tried 2 times (retry limit: 1):
1. **Attempt 0:** Failed after 5 seconds (06:53:22Z → 06:53:27Z)
2. **Attempt 1:** Failed after 5 seconds (06:53:32Z → 06:53:37Z)

Both attempts failed with the same error: "main: Error (exit code 1)"

### Workflow Configuration
- **Template:** openbao-read-test
- **Container:** openbao/openbao:2.6.1
- **Service Account:** argo-workflow
- **Target:** http://openbao.external-secrets.svc.cluster.local:8200
- **Test Path:** secret/monitoring/test

### Secondary Workflow: openbao-read-test-live-logs-5vmbg
- **Status:** Failed ❌
- **Completion:** 2026-08-14T06:53:41Z
- **Duration:** 20 seconds
- **Error Message:** "No more retries left"

## Log Capture Status
✅ **Workflow reached terminal state** - Both workflows completed (Failed)
✅ **Execution details captured** - Full workflow metadata stored
⚠️ **Pod logs unavailable** - Pods deleted by podGC: OnPodCompletion policy
✅ **Structured analysis created** - JSON file with error analysis and next steps

## Files Created
- `/home/coding/SEAM/logs/workflow-logs/openbao-read-test-manual-d6zg6-2026-08-14.json` - Complete workflow execution details
- `/home/coding/SEAM/logs/workflow-logs/workflow-execution-summary-2026-08-14.md` - This summary

## Conclusion
The workflows have reached terminal state (Failed) and execution details have been captured and stored. The consistent failure pattern across both retry attempts suggests a systemic issue with OpenBao connectivity, authentication, or permissions rather than a transient failure.
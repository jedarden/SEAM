# ArgoCD corpus completeness

This checklist compares the routes cataloged in [`notes/bf-3w8n.md`](../../notes/bf-3w8n.md) with the captured request/response pairs in [`corpus.json`](corpus.json). The capture was recorded on 2026-08-19 from the ArgoCD read-only proxy.

## Route coverage

| Identified route | Captured entry or entries | Status | Scenario |
| --- | --- | --- | --- |
| `GET /api/v1/applications` | `applications-list-success-get`; `applications-list-empty-get` | Present | Full application list and an empty result for a missing `name` filter |
| `GET /api/v1/applications/{app-name}` | `application-detail-success-actualbudget-nina-ns-ardenone-cluster-get`; `application-detail-error-missing-get` | Present | Existing application detail and missing-application error |
| `GET /api/v1/clusters` | `clusters-list-success-get`; `clusters-list-empty-get` | Present | Full cluster list and an empty result for a missing `server` filter |
| `GET /api/v1/projects` | `projects-list-success-get` | Present | Project list; this route was identified in the catalog as documented, although not found in active callers |
| `GET /api/v1/repositories` | `repositories-list-success-get` | Present | Repository list; this route was identified in the catalog as documented, although not found in active callers |

All five identified route shapes are present. The application and cluster list routes also have deterministic negative/filter scenarios, and the application detail route has both success and error behavior.

The catalog's older `tools/diffharness/testdata/corpus-argocd.json` is a two-entry schema fixture, not the live capture under review. The live capture supersedes its placeholder application name and includes the additional documented routes above. The `sync`, `manifest`, and repository-detail examples in `README.md` are not routes identified by `bf-3w8n` and are not claimed as covered here.

## Corpus file inventory

| File | Description |
| --- | --- |
| `corpus.json` | The live eight-entry request/response corpus used for differential replay |
| `corpus-template.json` | Schema/template example for creating future ArgoCD corpus entries; not a live capture |
| `applications-list.json` | Response snapshot for a successful full application list |
| `applications-list-empty.json` | Response snapshot for an empty application list after a missing-name filter |
| `application-detail-actualbudget-nina-ns-ardenone-cluster.json` | Response snapshot for the captured live application detail |
| `application-detail-missing-error.json` | Response snapshot for a missing-application error |
| `clusters-list.json` | Response snapshot for a successful full cluster list |
| `clusters-list-empty.json` | Response snapshot for an empty cluster list after a missing-server filter |
| `projects-list.json` | Response snapshot for a successful project list |
| `repositories-list.json` | Response snapshot for a successful repository list |

## Completeness and parseability

The eight entries in `corpus.json` were checked for:

- a non-empty request method and path;
- a response object with a valid HTTP status and response headers;
- a present, valid base64-encoded response body; and
- a response body that parses as JSON.

The companion response snapshots are checked against the corresponding decoded response in `corpus.json`:

| File | Scenario |
| --- | --- |
| `applications-list.json` | Successful full application list |
| `applications-list-empty.json` | Empty application list after a missing-name filter |
| `application-detail-actualbudget-nina-ns-ardenone-cluster.json` | Successful detail for the captured live application |
| `application-detail-missing-error.json` | Permission-denied response for a missing application |
| `clusters-list.json` | Successful full cluster list |
| `clusters-list-empty.json` | Empty cluster list after a missing-server filter |
| `projects-list.json` | Successful project list |
| `repositories-list.json` | Successful repository list |

The snapshots are response-body conveniences; the complete request plus response record is the matching entry in `corpus.json`.

## Mock-response sample

`TestArgoCDCapturedCorpusEntriesAreCompleteAndMockable` decodes the captured `applications-list-empty-get` response, serves it through an `httptest` HTTP server with its captured status and headers, and parses the returned JSON. This is the representative workflow fragment developers can use when building a route mock.

Run the validation with:

```sh
go test ./corpus
```

The executable checks are in [`corpus_integrity_test.go`](../corpus_integrity_test.go). Historical `twitterapi-proxy` and `zai-proxy` files remain request-only replay inputs; they are outside this ArgoCD capture-completeness comparison.

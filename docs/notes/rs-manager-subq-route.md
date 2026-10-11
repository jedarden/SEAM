# rs-manager Subquadratic route

SEAM's rs-manager deployment exposes the native Subquadratic Decisions API
through the `subq` route owner:

- `GET /subq/v1/models` probes the provider and lists the available models.
- `POST /subq/v1/systemone` forwards the native Decisions API request.

The upstream is `https://api.subq.ai`, with `/subq` stripped before dispatch.
The bearer credential is supplied by `SEAM_CRED_SUBQ_API_KEY`, which is mirrored
by the `seam-provider-credentials` ExternalSecret from the OpenBao path
`rs-manager/rs-manager/subq/api-key`. SEAM has no OpenBao read grant for this
provider key. The value is never written to this repository or emitted in
logs, responses, or health output.

The public preview key supports the Decisions API (`systemone`), not the
OpenAI-compatible chat-completions endpoint. Rotate the OpenBao value and let
the ExternalSecret plus Stakater Reloader restart the pod; the env source is
snapshotted when SEAM starts.

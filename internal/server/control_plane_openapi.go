package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// controlPlaneDocsPath is the caller-listener endpoint that serves the
// control-plane OpenAPI document built here.
const controlPlaneDocsPath = "/docs/control-plane"

// This file is the machine-readable contract of SEAM's own control plane: the
// reserved endpoints the gateway serves itself, as opposed to the upstream API
// it proxies. /docs and /openapi.json document the upstream spec from the
// loaded fragments; the document built here describes the gateway's own
// surface — /whoami, /scopes, /changes, /api/v1/tailscale/ephemeral-key and
// the documentation endpoints — and is compiled into the binary so the
// control-plane contract stays readable even when no fragment loaded.
//
// The document is assembled from the same source of truth as the handlers:
// every status code, error code, scope name and response field below was read
// off the handler that produces it. controlPlaneOpenAPIContractTest pins that
// the document names the endpoints the bead contract requires, that every
// error response references the shared ErrorResponse envelope component, and
// that the envelope's error enum is exactly the public taxonomy in errors.go.

// controlPlaneOpenAPIDocument builds the OpenAPI 3.0 document served by
// docsControlPlaneHandler. It depends on nothing but the server's base URL,
// so it renders identically with an empty spec, no fragments, or no upstream.
func (s *Server) controlPlaneOpenAPIDocument() map[string]interface{} {
	return map[string]interface{}{
		"openapi": "3.0.3",
		"info": map[string]interface{}{
			"title":   "SEAM Control Plane API",
			"version": "1.0.0",
			"description": "Contracts for the SEAM gateway's own control-plane endpoints: " +
				"identity, scopes, change history, ephemeral key issuance, and the " +
				"self-documenting surfaces. This document is served at " + controlPlaneDocsPath +
				"; the upstream API the gateway proxies is documented separately at /docs " +
				"and /openapi.json, scope-filtered to the caller. Every error, on every " +
				"endpoint in this document, uses the ErrorResponse envelope component.",
		},
		"servers": []interface{}{
			map[string]interface{}{"url": s.controlPlaneBaseURL()},
		},
		"tags": []interface{}{
			map[string]interface{}{"name": "identity", "description": "Resolved caller identity and scope correlation"},
			map[string]interface{}{"name": "scopes", "description": "The scope map the gateway enforces and reports"},
			map[string]interface{}{"name": "changes", "description": "Route-contract and visibility changes between spec versions"},
			map[string]interface{}{"name": "keys", "description": "Ephemeral Tailscale auth key issuance for NEEDLE workers"},
			map[string]interface{}{"name": "documentation", "description": "The self-documenting surfaces themselves"},
		},
		"security": []interface{}{},
		"x-seam-authentication": map[string]interface{}{
			"mechanism": "tailscale-whois",
			"description": "No control-plane endpoint accepts a caller-presented credential. " +
				"Stage 3 of the request pipeline resolves the caller from the inbound " +
				"tailnet connection via Tailscale WhoIs and derives scope claims from the " +
				"node's grant capabilities. An unresolvable caller is default-denied with " +
				"403 forbidden before any handler runs; the infrastructure probes " +
				"(/_seam/health, /_seam/healthz, /_seam/readyz, /_seam/metrics) are the one " +
				"exemption, because they arrive over the pod network where no identity can " +
				"exist. Stage 2 strips inbound X-SEAM-* headers, so a client cannot " +
				"assert identity or scope state by header.",
		},
		"x-seam-listeners": map[string]interface{}{
			"caller": "The caller-facing port. Serves every endpoint in this document.",
			"operator": "A separate operator-only port (/_seam/metrics, /config/status, " +
				"/health/credentials, /health/upstreams, /_seam/capture/*, /_seam/cache/*). " +
				"Those endpoints are not part of this document; they are gated on the " +
				"seam:ops:read scope by the operator scope middleware.",
		},
		"paths": map[string]interface{}{
			"/whoami":                         whoamiPathItem(),
			"/scopes":                         scopesPathItem(),
			"/changes":                        changesPathItem(),
			"/api/v1/tailscale/ephemeral-key": ephemeralKeyPathItem(),
			"/docs":                           docsPathItem(),
			"/docs/route":                     docsRoutePathItem(),
			"/docs/paths":                     docsPathsPathItem(),
			"/openapi.json":                   openapiJSONPathItem(),
		},
		"components": map[string]interface{}{
			"schemas": controlPlaneSchemaComponents(),
		},
	}
}

// controlPlaneBaseURL returns the caller-facing base URL for the servers array.
func (s *Server) controlPlaneBaseURL() string {
	if s.config != nil && s.config.BaseURL != "" {
		return s.config.BaseURL
	}
	return "http://localhost:8080"
}

// controlPlaneErrorCodeEnum returns the sorted public error-code taxonomy. It
// is derived from HTTPStatusMapping so the document's enum can never drift
// from the codes the error writers actually normalize against.
func controlPlaneErrorCodeEnum() []string {
	codes := make([]string, 0, len(HTTPStatusMapping))
	for code := range HTTPStatusMapping {
		codes = append(codes, string(code))
	}
	sort.Strings(codes)
	return codes
}

// jsonResponse builds an application/json response entry.
func jsonResponse(description string, schema map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"description": description,
		"content": map[string]interface{}{
			"application/json": map[string]interface{}{"schema": schema},
		},
	}
}

// errorResponseRef builds a response entry bound to the shared ErrorResponse
// envelope component. Every non-2xx control-plane response goes through
// ErrorResponse.Write, so every non-2xx entry in this document references it.
func errorResponseRef(description string) map[string]interface{} {
	return jsonResponse(description, map[string]interface{}{
		"$ref": "#/components/schemas/ErrorResponse",
	})
}

// methodNotAllowedResponse is the 405 every handler in this document returns
// for a wrong method on the path.
func methodNotAllowedResponse() map[string]interface{} {
	return errorResponseRef("Wrong method on this path. The handler writes the shared " +
		"error envelope with error=method_not_allowed.")
}

// identityDeniedResponse is the 403 stage 3 produces before a handler runs.
func identityDeniedResponse() map[string]interface{} {
	return errorResponseRef("Stage 3 identity resolution failed: the connection resolved " +
		"to no tailnet identity. Default-deny fires before the handler runs, with " +
		"error=forbidden and message \"Identity resolution failed\".")
}

// scopeVersionHeader describes the correlation header the scope-version
// middleware stamps on every caller-listener response.
func scopeVersionHeader() map[string]interface{} {
	return map[string]interface{}{
		"description": "SHA-256 hash of the caller's sorted, normalized effective scope " +
			"set. Correlates a response with a scope state; changes whenever the " +
			"caller's grants change.",
		"schema": map[string]interface{}{"type": "string"},
	}
}

func whoamiPathItem() map[string]interface{} {
	return map[string]interface{}{
		"get": map[string]interface{}{
			"tags":            []interface{}{"identity"},
			"summary":         "Return the resolved caller identity and effective scopes",
			"description":     "Caller listener. Resolves to the identity stage 3 put in the request context: node identity from Tailscale WhoIs, effective scopes from the grant capabilities, and the current X-SEAM-Scope-Version hash. Any resolved identity may call it; the response is the caller's own view and withholds nothing, so it carries no scope requirement.",
			"x-seam-listener": "caller",
			"responses": map[string]interface{}{
				"200": map[string]interface{}{
					"description": "The resolved identity, effective scopes, and scope-version hash.",
					"headers":     map[string]interface{}{"X-SEAM-Scope-Version": scopeVersionHeader()},
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{"$ref": "#/components/schemas/WhoamiResponse"},
						},
					},
				},
				"403": identityDeniedResponse(),
				"405": methodNotAllowedResponse(),
			},
		},
	}
}

func scopesPathItem() map[string]interface{} {
	return map[string]interface{}{
		"get": map[string]interface{}{
			"tags":            []interface{}{"scopes"},
			"summary":         "Return the derived scope map, filtered to the caller by default",
			"description":     "Caller listener. Merges two sources: scopes derived from the loaded fragments' x-required-scope declarations (source=spec, mapped to the routes that require them) and the compiled-in control-plane set (source=builtin). Default output is filtered to scopes the caller holds; ?all=1 lifts the filter for callers holding seam:scopes:read-all.",
			"x-seam-listener": "caller",
			"parameters": []interface{}{
				map[string]interface{}{
					"name":        "all",
					"in":          "query",
					"required":    false,
					"description": "Set to 1 to return the unfiltered scope map. Requires the seam:scopes:read-all scope; without it the endpoint returns 403 forbidden with details.required_scope=seam:scopes:read-all and the filter is not lifted.",
					"schema":      map[string]interface{}{"type": "string", "enum": []interface{}{"1"}},
				},
			},
			"responses": map[string]interface{}{
				"200": map[string]interface{}{
					"description": "The scope map. filtered=true means the map was reduced to the caller's own scopes.",
					"headers":     map[string]interface{}{"X-SEAM-Scope-Version": scopeVersionHeader()},
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{"$ref": "#/components/schemas/ScopesResponse"},
						},
					},
				},
				"403": errorResponseRef("Two distinct denials use this status: the ?all=1 gate (error=forbidden, details.required_scope=seam:scopes:read-all) and stage-3 identity default-deny."),
				"405": methodNotAllowedResponse(),
			},
		},
	}
}

func changesPathItem() map[string]interface{} {
	return map[string]interface{}{
		"get": map[string]interface{}{
			"tags":            []interface{}{"changes"},
			"summary":         "Return route-contract and visibility changes between spec versions",
			"description":     "Caller listener. Compares the current spec against a prior version held in the in-memory ring buffer. Level 1 lists routes with contract and visibility indicators; level 2 adds field-level diffs. Route entries are filtered to the caller's visible scopes. With no since parameter the oldest version still in the ring buffer is used; a since the buffer has evicted yields 200 with since_known=false and no route entries — it is not an error.",
			"x-seam-listener": "caller",
			"parameters": []interface{}{
				map[string]interface{}{
					"name":        "level",
					"in":          "query",
					"required":    false,
					"description": "Diff depth. 1 (default) lists routes with change indicators; 2 adds field_diff entries per route. Any other value returns 400 bad_request with details.valid_levels=\"1, 2\".",
					"schema":      map[string]interface{}{"type": "string", "enum": []interface{}{"1", "2"}, "default": "1"},
				},
				map[string]interface{}{
					"name":        "since",
					"in":          "query",
					"required":    false,
					"description": "Spec hash to diff against. Defaults to the oldest version in the ring buffer; an unknown or evicted hash yields since_known=false rather than an error.",
					"schema":      map[string]interface{}{"type": "string"},
				},
				map[string]interface{}{
					"name":        "scope-since",
					"in":          "query",
					"required":    false,
					"description": "X-SEAM-Scope-Version hash to diff the caller's scope state against. When present, the response gains scope_changes entries with change_type granted or revoked.",
					"schema":      map[string]interface{}{"type": "string"},
				},
			},
			"responses": map[string]interface{}{
				"200": map[string]interface{}{
					"description": "The change set between the requested versions.",
					"headers":     map[string]interface{}{"X-SEAM-Scope-Version": scopeVersionHeader()},
					"content": map[string]interface{}{
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{"$ref": "#/components/schemas/ChangesResponse"},
						},
					},
				},
				"400": errorResponseRef("error=bad_request with details.valid_levels when level is neither 1 nor 2."),
				"403": identityDeniedResponse(),
				"405": methodNotAllowedResponse(),
				"503": errorResponseRef("error=service_unavailable: no current spec is loaded into the ring buffer, so there is nothing to diff against."),
			},
		},
	}
}

func ephemeralKeyPathItem() map[string]interface{} {
	return map[string]interface{}{
		"post": map[string]interface{}{
			"tags":             []interface{}{"keys"},
			"summary":          "Create an ephemeral Tailscale auth key for a NEEDLE worker",
			"description":      "Caller listener. The one control-plane endpoint that takes a request body and the only one that issues a credential. Requires the seam:tailscale:key-create scope. The response body carries the live tskey: it is a secret, transport-protected by the tailnet, and must never be logged or echoed. Creation goes through the Tailscale API client's cache and hold-down, so a failure window returns 503 with a retry_after hint rather than hammering the upstream API.",
			"x-seam-listener":  "caller",
			"x-required-scope": []interface{}{"seam:tailscale:key-create"},
			"requestBody": map[string]interface{}{
				"required": true,
				"content": map[string]interface{}{
					"application/json": map[string]interface{}{
						"schema": map[string]interface{}{"$ref": "#/components/schemas/WorkerKeyRequest"},
					},
				},
			},
			"responses": map[string]interface{}{
				"200": jsonResponse("The created key. key is the live tskey secret.", map[string]interface{}{
					"$ref": "#/components/schemas/EphemeralKeyResponse",
				}),
				"400": errorResponseRef("error=bad_request: the body was not valid JSON (details.error) or worker_id was empty (details.field=worker_id)."),
				"403": errorResponseRef("Two distinct denials: the scope gate (error=forbidden, details.required_scope=seam:tailscale:key-create) and stage-3 identity default-deny."),
				"405": methodNotAllowedResponse(),
				"500": errorResponseRef("error=internal_server_error: the Tailscale client is misconfigured (no API key or tailnet) or key creation failed; details.error carries the client-side reason."),
				"503": errorResponseRef("error=service_unavailable: the Tailscale client is not initialized (details.reason) or is inside its post-failure hold-down window (details.retry_after, seconds)."),
			},
		},
	}
}

func docsPathItem() map[string]interface{} {
	return map[string]interface{}{
		"get": map[string]interface{}{
			"tags":            []interface{}{"documentation"},
			"summary":         "Render the upstream API reference, or return the filtered spec as JSON",
			"description":     "Caller listener. With Accept: application/json, returns the caller's scope-filtered upstream OpenAPI document raw, stamped with X-SEAM-Spec-Version, X-Spec-Version and X-SEAM-API-Version. Any other Accept renders the Scalar reference page (HTML) with that same spec embedded, plus the Agentation feedback toolbar. This document — the control-plane contract — is a separate surface at " + controlPlaneDocsPath + ".",
			"x-seam-listener": "caller",
			"responses": map[string]interface{}{
				"200": map[string]interface{}{
					"description": "The Scalar HTML page, or the raw filtered OpenAPI JSON when Accept names application/json.",
					"content": map[string]interface{}{
						"text/html": map[string]interface{}{"schema": map[string]interface{}{"type": "string"}},
						"application/json": map[string]interface{}{
							"schema": map[string]interface{}{"type": "object", "description": "The caller's scope-filtered upstream OpenAPI document."},
						},
					},
				},
				"403": identityDeniedResponse(),
				"405": methodNotAllowedResponse(),
				"500": errorResponseRef("error=spec_load_failed: the merged upstream spec could not be loaded or is not valid JSON."),
			},
		},
	}
}

func docsRoutePathItem() map[string]interface{} {
	return map[string]interface{}{
		"get": map[string]interface{}{
			"tags":            []interface{}{"documentation"},
			"summary":         "Return the documentation slice for one upstream route",
			"description":     "Caller listener. Resolves one path (and optionally one method) against the served upstream document. With Accept: text/html it redirects (302) to the /docs anchor instead of returning JSON. Scope filtering follows the 404 oracle: a route the caller's filtered spec does not grant returns 404 byte-identical to a route that never existed; a visible path whose requested method is missing returns 403 visible-but-not-invocable.",
			"x-seam-listener": "caller",
			"parameters": []interface{}{
				map[string]interface{}{
					"name":        "path",
					"in":          "query",
					"required":    true,
					"description": "The OpenAPI path template to look up. Absent or empty returns 400 error=missing_required_parameter with details.parameter=path.",
					"schema":      map[string]interface{}{"type": "string"},
				},
				map[string]interface{}{
					"name":        "method",
					"in":          "query",
					"required":    false,
					"description": "HTTP method, case-insensitive. Omitted means every method on the path.",
					"schema":      map[string]interface{}{"type": "string"},
				},
				map[string]interface{}{
					"name":        "version",
					"in":          "query",
					"required":    false,
					"description": "API version matching ^v[1-9][0-9]*$ or _unversioned; anything else returns 400 error=invalid_version_parameter with the expected grammar in details.expected_format.",
					"schema":      map[string]interface{}{"type": "string", "default": "_unversioned"},
				},
			},
			"responses": map[string]interface{}{
				"200": jsonResponse("The raw path item from the served document, plus per-version entries and deprecation status where the route table declares them.", map[string]interface{}{
					"type":        "object",
					"description": "Route documentation slice; shape mirrors the served upstream path item.",
				}),
				"302": map[string]interface{}{
					"description": "Accept: text/html. Location is /docs#<path-method anchor>.",
				},
				"400": errorResponseRef("error=missing_required_parameter (no path) or error=invalid_version_parameter (bad version grammar)."),
				"403": errorResponseRef("error=forbidden: the path is visible to the caller but the requested method is not invocable — the response names the gap and carries a grant snippet."),
				"404": errorResponseRef("error=not_found: the route is outside the caller's filtered spec. Byte-identical to a nonexistent route, so the response cannot leak that a route exists."),
				"405": methodNotAllowedResponse(),
			},
		},
	}
}

func docsPathsPathItem() map[string]interface{} {
	return map[string]interface{}{
		"get": map[string]interface{}{
			"tags":            []interface{}{"documentation"},
			"summary":         "Return every path with its last-2xx status",
			"description":     "Caller listener. Observability over the served upstream surface: metadata (spec and API version, total path count) plus a per-path map of last-2xx status from the tracker. Cache-Control: no-store.",
			"x-seam-listener": "caller",
			"responses": map[string]interface{}{
				"200": jsonResponse("Path statuses plus metadata.", map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"metadata": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"description":  map[string]interface{}{"type": "string"},
								"spec_version": map[string]interface{}{"type": "string"},
								"api_version":  map[string]interface{}{"type": "string"},
								"total_paths":  map[string]interface{}{"type": "integer"},
							},
						},
						"paths": map[string]interface{}{
							"type":                 "object",
							"additionalProperties": true,
							"description":          "Map of upstream path to its last-2xx status entry.",
						},
					},
				}),
				"403": identityDeniedResponse(),
				"405": methodNotAllowedResponse(),
			},
		},
	}
}

func openapiJSONPathItem() map[string]interface{} {
	return map[string]interface{}{
		"get": map[string]interface{}{
			"tags":            []interface{}{"documentation"},
			"summary":         "Return the caller's scope-filtered upstream OpenAPI document",
			"description":     "Caller listener. The machine-readable twin of /docs. A version query that looks like a spec hash (longer than an API version) delegates to the spec archive instead, returning that archived version. The control-plane document served at " + controlPlaneDocsPath + " is deliberately not merged into this response: this surface describes the upstream API, and merging gateway paths into it would change its shape under every pinned consumer.",
			"x-seam-listener": "caller",
			"parameters": []interface{}{
				map[string]interface{}{
					"name":        "version",
					"in":          "query",
					"required":    false,
					"description": "API version, or a spec hash to fetch from the archive.",
					"schema":      map[string]interface{}{"type": "string"},
				},
			},
			"responses": map[string]interface{}{
				"200": jsonResponse("The scope-filtered upstream OpenAPI document (or the archived one requested).", map[string]interface{}{
					"type":        "object",
					"description": "An OpenAPI 3 document.",
				}),
				"403": identityDeniedResponse(),
				"405": methodNotAllowedResponse(),
			},
		},
	}
}

// controlPlaneSchemaComponents returns the components/schemas section: the
// shared error envelope every endpoint normalizes through, plus one schema
// per endpoint response and request body.
func controlPlaneSchemaComponents() map[string]interface{} {
	return map[string]interface{}{
		"ErrorResponse": map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"error", "message"},
			"description": "The one error envelope for the whole gateway. Written by " +
				"ErrorResponse.Write, which also stamps Cache-Control: no-store and " +
				"X-Content-Type-Options: nosniff on every error response. An unknown " +
				"error code is normalized to internal_server_error with a generic " +
				"message before serialization, so the enum below is closed.",
			"properties": map[string]interface{}{
				"error": map[string]interface{}{
					"type": "string",
					"enum": controlPlaneErrorCodeEnum(),
					"description": "Stable machine-readable identifier. The value is public " +
						"contract; the enum is exactly the taxonomy in HTTPStatusMapping.",
				},
				"message": map[string]interface{}{
					"type":        "string",
					"description": "Human-readable summary. Never carries an internal cause; causes go to the server log under the request_id.",
				},
				"details": map[string]interface{}{
					"type":                 "object",
					"additionalProperties": true,
					"description":          "Caller-safe context (required_scope, field, valid_levels, retry_after, ...).",
				},
				"validation_errors": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"$ref": "#/components/schemas/ValidationFieldError"},
					"description": "Present only on request-validation failures; its presence never changes the base envelope.",
				},
				"docs_url": map[string]interface{}{
					"type":        "string",
					"description": "Documentation pointer for the failure (e.g. /docs, /docs/route).",
				},
				"request_id": map[string]interface{}{
					"type":        "string",
					"description": "Correlates the response with the server log record. Mirrors the X-Request-ID response header.",
				},
			},
		},
		"ValidationFieldError": map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"field", "expected_shape", "reason"},
			"properties": map[string]interface{}{
				"field":          map[string]interface{}{"type": "string"},
				"expected_shape": map[string]interface{}{"type": "string"},
				"actual":         map[string]interface{}{"type": "string"},
				"reason":         map[string]interface{}{"type": "string"},
				"line":           map[string]interface{}{"type": "integer"},
				"column":         map[string]interface{}{"type": "integer"},
			},
		},
		"IdentityObject": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"node_key":  map[string]interface{}{"type": "string", "description": "Stable Tailscale node key."},
				"node_name": map[string]interface{}{"type": "string", "description": "Calling node hostname."},
				"user":      map[string]interface{}{"type": "string", "description": "User identity, when the caller is a user."},
				"tags":      map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Tailscale tags on the node."},
			},
		},
		"WhoamiResponse": map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"identity", "effective_scopes", "scope_version", "resolved"},
			"properties": map[string]interface{}{
				"identity":         map[string]interface{}{"$ref": "#/components/schemas/IdentityObject"},
				"effective_scopes": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				"scope_version":    map[string]interface{}{"type": "string", "description": "Same value as the X-SEAM-Scope-Version response header."},
				"resolved":         map[string]interface{}{"type": "boolean", "description": "False only for the defensive fallback; an unresolved caller is denied at stage 3 and never reaches this handler."},
			},
		},
		"ScopeInfo": map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"routes", "source"},
			"properties": map[string]interface{}{
				"routes": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string"},
					"description": "\"METHOD <path>\" entries for spec-derived scopes, or the literal \"<control-plane>\" for builtin scopes.",
				},
				"source": map[string]interface{}{"type": "string", "enum": []interface{}{"spec", "builtin"}},
			},
		},
		"ScopesResponse": map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"scopes", "filtered", "total_scopes", "returned", "effective_count"},
			"properties": map[string]interface{}{
				"scopes": map[string]interface{}{
					"type":                 "object",
					"additionalProperties": map[string]interface{}{"$ref": "#/components/schemas/ScopeInfo"},
				},
				"filtered":        map[string]interface{}{"type": "boolean", "description": "true unless ?all=1 lifted the filter."},
				"total_scopes":    map[string]interface{}{"type": "integer", "description": "Count across both sources, before filtering."},
				"returned":        map[string]interface{}{"type": "integer", "description": "Count actually returned, after filtering."},
				"effective_count": map[string]interface{}{"type": "integer", "description": "Scopes the caller holds."},
			},
		},
		"RouteChange": map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"path", "verb", "diff_url", "docs_url"},
			"properties": map[string]interface{}{
				"path": map[string]interface{}{"type": "string"},
				"verb": map[string]interface{}{"type": "string"},
				"contract_kinds": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string", "enum": []interface{}{"added", "removed", "params-changed", "response-changed", "deprecated"}},
					"description": "Level 1 indicator set; absent when nothing changed.",
				},
				"visibility_kinds": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string", "enum": []interface{}{"granted", "revoked"}},
					"description": "How the change moved the route relative to the caller's scopes.",
				},
				"diff_url": map[string]interface{}{"type": "string"},
				"docs_url": map[string]interface{}{"type": "string"},
				"field_diff": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"$ref": "#/components/schemas/FieldDiffEntry"},
					"description": "Level 2 only.",
				},
			},
		},
		"FieldDiffEntry": map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"field", "change"},
			"properties": map[string]interface{}{
				"field":     map[string]interface{}{"type": "string", "description": "Field path, e.g. parameters[0].description."},
				"old_value": map[string]interface{}{"type": "string"},
				"new_value": map[string]interface{}{"type": "string"},
				"change":    map[string]interface{}{"type": "string", "enum": []interface{}{"added", "removed", "changed"}},
			},
		},
		"ScopeChange": map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"scopes", "change_type"},
			"properties": map[string]interface{}{
				"scopes":      map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				"change_type": map[string]interface{}{"type": "string", "enum": []interface{}{"granted", "revoked"}},
			},
		},
		"ChangesResponse": map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"since_spec", "since_known", "current_spec", "current_version", "query", "routes", "route_count"},
			"properties": map[string]interface{}{
				"since_spec":      map[string]interface{}{"type": "string"},
				"since_known":     map[string]interface{}{"type": "boolean", "description": "false when the since hash is unknown or evicted from the ring buffer; routes is then empty, not an error."},
				"current_spec":    map[string]interface{}{"type": "string"},
				"current_version": map[string]interface{}{"type": "string"},
				"query": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"level":       map[string]interface{}{"type": "string"},
						"since":       map[string]interface{}{"type": "string"},
						"scope_since": map[string]interface{}{"type": "string"},
					},
				},
				"routes":        map[string]interface{}{"type": "array", "items": map[string]interface{}{"$ref": "#/components/schemas/RouteChange"}},
				"route_count":   map[string]interface{}{"type": "integer"},
				"scope_changes": map[string]interface{}{"type": "array", "items": map[string]interface{}{"$ref": "#/components/schemas/ScopeChange"}, "description": "Present only when scope-since was supplied."},
			},
		},
		"WorkerKeyRequest": map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"worker_id"},
			"properties": map[string]interface{}{
				"worker_id": map[string]interface{}{"type": "string", "description": "NEEDLE worker name the key is created for; becomes the key description."},
			},
		},
		"EphemeralKeyResponse": map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"key", "id", "expires", "description"},
			"properties": map[string]interface{}{
				"key":         map[string]interface{}{"type": "string", "description": "The live tskey. Treat as a secret: never log, never echo."},
				"id":          map[string]interface{}{"type": "string", "description": "Tailscale's key identifier, for later revocation."},
				"expires":     map[string]interface{}{"type": "string", "format": "date-time", "description": "RFC 3339 expiry."},
				"description": map[string]interface{}{"type": "string"},
			},
		},
	}
}

// renderControlPlaneDocsHTML embeds the control-plane document in the shared
// documentation page shell.
func renderControlPlaneDocsHTML(specJSON string) string {
	return docsHTMLShell("SEAM Control Plane API Documentation",
		"Contracts for SEAM's control-plane endpoints: identity, scopes, changes, ephemeral keys, and the documentation surfaces themselves.",
		specJSON)
}

// docsControlPlaneHandler serves the compiled-in control-plane OpenAPI
// document. Accept: application/json returns the raw document; anything else
// renders it through the Scalar page shell shared with /docs. The document is
// built in-process rather than loaded from fragments, so the control-plane
// contract stays served even when the upstream spec is empty or broken.
func (s *Server) docsControlPlaneHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		MethodNotAllowed("Only GET method is allowed").Write(w, r)
		return
	}

	payload, err := json.Marshal(s.controlPlaneOpenAPIDocument())
	if err != nil {
		NewErrorResponse(ErrCodeInternalServer, "Failed to render control-plane specification").Write(w, r)
		return
	}

	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// Escape the JSON for safe embedding in an HTML script tag, exactly as
	// docsHandler does for the upstream spec.
	_, _ = w.Write([]byte(renderControlPlaneDocsHTML(strings.ReplaceAll(string(payload), "</script>", `<\/script>`))))
}

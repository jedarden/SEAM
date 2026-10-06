package corpus

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// captureRedactionMarker is the marker the gateway's capture middleware (and,
// at replay, the diffharness comparator) substitutes for a credential value —
// internal/server RedactedSecret and internal/compare RedactionToken. This
// module is standalone, so the value is mirrored here; the promotion lifecycle
// below is exactly the kind of one-sided drift such a mirror risks, which is
// why the assertions compare against this constant rather than inline
// literals.
const captureRedactionMarker = "[REDACTED-BY-SEAM]"

// standaloneVerbatimToken is the fake bearer credential the standalone-capture
// fixture below records verbatim. It is deliberately not a credential — it is
// a marker string whose only job is to prove that promotion scrubbed it.
const standaloneVerbatimToken = "Bearer seam-capture-records-verbatim-not-a-credential"

// rawMiddlewareCapture is the on-disk shape the gateway's capture middleware
// writes (docs/capture_testing.md promotion runbook, step 2 table): producer
// defaults for service and incumbent, a capturedAt re-stamped at save time
// (later than the entry's own capture), per-entry capture-time response, no
// secrets — and the credential locations already scrubbed to the marker, the
// one redaction the middleware performs itself.
const rawMiddlewareCapture = `{
  "schema": "seam-diff-corpus/v1",
  "service": "seam",
  "incumbent": "seam-incumbent",
  "capturedAt": "2026-09-28T10:05:00Z",
  "description": "seam corpus captured from incumbent proxy",
  "entries": [
    {
      "id": "api-v1-applications-get",
      "timestamp": "2026-09-28T10:00:00Z",
      "description": "GET /api/v1/applications",
      "request": {
        "method": "GET",
        "path": "/api/v1/applications",
        "query": "limit=50&api_key=%5BREDACTED-BY-SEAM%5D",
        "headers": {
          "Accept": ["application/json"],
          "Authorization": ["[REDACTED-BY-SEAM]"]
        },
        "bodyContentType": ""
      },
      "response": {
        "statusCode": 200,
        "headers": { "Content-Type": ["application/json"] },
        "bodyB64": "eyJpdGVtcyI6W119",
        "bodyContentType": "application/json"
      }
    }
  ]
}`

// rawStandaloneCapture is the on-disk shape the standalone seam-capture proxy
// writes: capture-argocd.sh passes the retired `argocd` service token and a
// real incumbent URL. Its fixed sensitive header names are scrubbed before
// persistence, but an unrecognised header and query value remain verbatim
// because the standalone tool has no route-fragment metadata. Secrets are
// likewise never populated (an explicit TODO in its source).
const rawStandaloneCapture = `{
  "schema": "seam-diff-corpus/v1",
  "service": "argocd",
  "incumbent": "https://argocd-ro-ardenone-manager-ts.ardenone.com:8444",
  "capturedAt": "2026-09-28T11:00:00Z",
  "description": "argocd corpus captured from incumbent proxy",
  "entries": [
    {
      "id": "api-v1-clusters-get",
      "timestamp": "2026-09-28T11:00:00Z",
      "description": "GET /api/v1/clusters",
      "request": {
        "method": "GET",
        "path": "/api/v1/clusters",
        "query": "api_key=standalone-capture-query-not-a-credential",
        "headers": {
          "Accept": ["application/json"],
          "Authorization": ["[REDACTED-BY-SEAM]"],
          "X-Capture-Token": ["` + standaloneVerbatimToken + `"]
        },
        "bodyContentType": ""
      },
      "response": {
        "statusCode": 200,
        "headers": {
          "Content-Type": ["application/json"],
          "Set-Cookie": ["[REDACTED-BY-SEAM]"],
          "X-Upstream-Token": ["fixture-promotion-response-sentinel"]
        },
        "bodyB64": "e30=",
        "bodyContentType": "application/json"
      }
    }
  ]
}`

// TestPromotionLifecycleConvertsCaptureToFixture pins the promotion runbook
// end to end (docs/capture_testing.md, "Promotion runbook: capture →
// fixture"): neither producer writes fixture-ready data, so the documented
// rewrite — canonical service token, the incumbent URL actually captured
// against, first-capture capturedAt, hand-written secret refs, replay
// expectations, capture-time responses dropped, verbatim credentials scrubbed
// — must turn both producer shapes into a corpus that loads clean and meets
// the fixture conventions. If the documented conversion stops matching what
// the loader and the checked-in-fixture walk enforce, this fails the
// diffharness module gate at fixture time instead of failing a future
// promotion by surprise.
func TestPromotionLifecycleConvertsCaptureToFixture(t *testing.T) {
	// Stage 1 — both raw captures load as their producer wrote them. The
	// loader's contract is deliberately narrow (schema, non-empty service,
	// unique IDs, in-base refs), so producer defaults pass it; everything
	// fixture-shaped about the file is the promotion's job, not the loader's.
	middlewareCapture := loadRawCorpus(t, "middleware capture", rawMiddlewareCapture)
	if middlewareCapture.Service != "seam" || middlewareCapture.Incumbent != "seam-incumbent" {
		t.Fatalf("middleware capture metadata = %q/%q, want the documented producer defaults seam/seam-incumbent",
			middlewareCapture.Service, middlewareCapture.Incumbent)
	}
	if middlewareCapture.CapturedAt != "2026-09-28T10:05:00Z" {
		t.Fatalf("middleware capture capturedAt = %q, want the save-time restamp — the fact step 2's capturedAt rule exists for", middlewareCapture.CapturedAt)
	}
	middlewareEntry := middlewareCapture.Entries[0]
	if middlewareEntry.Response == nil {
		t.Fatal("middleware capture entry lost its capture-time response — the raw producer shape must carry one for the promotion to strip")
	}
	if len(middlewareEntry.Secrets) != 0 {
		t.Fatalf("middleware capture entry carries secrets = %v, want none — neither producer populates them", middlewareEntry.Secrets)
	}
	if got := middlewareEntry.Request.Headers["Authorization"]; len(got) != 1 || got[0] != captureRedactionMarker {
		t.Fatalf("middleware capture Authorization = %v, want the capture-time redaction marker %q", got, captureRedactionMarker)
	}

	standaloneCapture := loadRawCorpus(t, "standalone capture", rawStandaloneCapture)
	if standaloneCapture.Service != "argocd" {
		t.Fatalf("standalone capture service = %q, want the retired argocd token capture-argocd.sh passes — the loader accepts it (service need only be non-empty), which is why the runbook's rewrite is a documented step rather than a loader rule", standaloneCapture.Service)
	}
	standaloneEntry := standaloneCapture.Entries[0]
	if got := standaloneEntry.Request.Headers["Authorization"]; len(got) != 1 || got[0] != captureRedactionMarker {
		t.Fatalf("standalone capture Authorization = %v, want the fixed-name redaction marker", got)
	}
	if got := standaloneEntry.Request.Headers["X-Capture-Token"]; len(got) != 1 || got[0] != standaloneVerbatimToken {
		t.Fatalf("standalone capture X-Capture-Token = %v, want the unrecognised header's verbatim record", got)
	}
	if !strings.Contains(standaloneEntry.Request.Query, "standalone-capture-query-not-a-credential") {
		t.Fatalf("standalone capture query = %q, want the unrecognised query value recorded verbatim", standaloneEntry.Request.Query)
	}

	// Stage 2 — the promotion. First the review step the standalone shape
	// exists to exercise: its verbatim credential is a promotion blocker, so
	// the review scrubs it to the marker the middleware would have written.
	standaloneEntry.Request.Headers["X-Capture-Token"] = []string{captureRedactionMarker}
	standaloneCapture.Entries[0].Request.Query = "api_key=%5BREDACTED-BY-SEAM%5D"

	// Then the metadata + convention rewrite (runbook step 2) for both
	// captures, gated through the fixture validations (step 3): save, and let
	// Load reject what the rewrite missed.
	middlewareFixture := promoteAndLoad(t, middlewareCapture, "promoted-middleware-fixture.json",
		"argocd-ro", "https://argocd-ro-ardenone-manager-ts.ardenone.com:8444",
		"2026-09-28T10:00:00Z", // first-capture time from the session, not the save-time restamp
		map[string]string{
			"api-v1-applications-get": "vault:" + DefaultVaultBaseDir + "/argocd-ro/ro-token",
		},
		nil)
	standaloneFixture := promoteAndLoad(t, standaloneCapture, "promoted-standalone-fixture.json",
		"argocd-ro", "https://argocd-ro-ardenone-manager-ts.ardenone.com:8444",
		"2026-09-28T11:00:00Z",
		map[string]string{
			"api-v1-clusters-get": "vault:" + DefaultVaultBaseDir + "/argocd-ro/ro-token",
		},
		[]string{"verbatim", "standalone-capture-query-not-a-credential", "fixture-promotion-response-sentinel"})

	// The checked-in lifecycle fixture is the reviewed result of this exact
	// standalone capture. Comparing the serialized/reloaded candidate with it
	// makes the persistence boundary executable: a future fixture can neither
	// retain the raw response nor silently skip the request redactions above.
	checkedInFixture, err := Load("../../testdata/capture-promotion-lifecycle.json")
	if err != nil {
		t.Fatalf("load checked-in promotion fixture: %v", err)
	}
	if !reflect.DeepEqual(standaloneFixture, checkedInFixture) {
		t.Fatalf("checked-in promotion fixture does not match the sanitized candidate:\nwant=%+v\n got=%+v", standaloneFixture, checkedInFixture)
	}

	// Stage 3 — the fixture conventions hold on what the documented
	// conversion produced.
	for name, fixture := range map[string]*Corpus{
		"middleware": middlewareFixture,
		"standalone": standaloneFixture,
	} {
		if fixture.Service != "argocd-ro" {
			t.Errorf("%s fixture service = %q, want the canonical argocd-ro token", name, fixture.Service)
		}
		if !fixture.HasReplayable() {
			t.Errorf("%s fixture has no replayable entries — the promotion must leave a replayable corpus", name)
		}
		for _, e := range fixture.Entries {
			if e.Response != nil {
				t.Errorf("%s fixture entry %q still carries a capture-time response — the convention drops it; replay collects fresh responses from both targets", name, e.ID)
			}
			if e.Expect == nil || len(e.Expect.IgnoreHeaders) == 0 {
				t.Errorf("%s fixture entry %q carries no replay expectation — a fixture retains request data and replay expectations", name, e.ID)
			}
			if len(e.Secrets) != 1 {
				t.Errorf("%s fixture entry %q has %d secrets, want the hand-written ref the promotion adds", name, e.ID, len(e.Secrets))
				continue
			}
			s := e.Secrets[0]
			if s.Ref != "vault:"+DefaultVaultBaseDir+"/argocd-ro/ro-token" {
				t.Errorf("%s fixture entry %q ref = %q, want it under the enforced base's argocd-ro segment", name, e.ID, s.Ref)
			}
			if s.InjectAs.Kind != "bearer" || s.InjectAs.Name != "" {
				t.Errorf("%s fixture entry %q injectAs = %+v, want kind bearer with no name", name, e.ID, s.InjectAs)
			}
		}
	}

	// Redaction survives the conversion: the middleware-scrubbed marker is
	// the expected fixture state for a credential location, and the escaped
	// query marker round-trips untouched.
	got := middlewareFixture.Entries[0].Request.Headers["Authorization"]
	if len(got) != 1 || got[0] != captureRedactionMarker {
		t.Errorf("promoted middleware fixture Authorization = %v, want the marker — promotion must not undo capture-time redaction", got)
	}
	if q := middlewareFixture.Entries[0].Request.Query; !strings.Contains(q, "api_key=%5BREDACTED-BY-SEAM%5D") {
		t.Errorf("promoted middleware fixture query = %q, want the escaped marker on api_key", q)
	}
}

// loadRawCorpus parses one raw producer capture through the production Load
// path, proving the loader accepts producer defaults as written.
func loadRawCorpus(t *testing.T, name, raw string) *Corpus {
	t.Helper()
	path := filepath.Join(t.TempDir(), "raw-capture.json")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	if len(c.Entries) != 1 {
		t.Fatalf("%s: got %d entries, want 1", name, len(c.Entries))
	}
	return c
}

// promoteAndLoad applies the promotion runbook's rewrite to a raw capture and
// gates the result through the fixture validations: canonical service token,
// the incumbent URL actually captured against, the first-capture timestamp,
// one hand-written secret ref per entry, a replay expectation per entry, and
// the fixture convention of dropping the capture-time response. mustNotContain
// lists substrings the committed fixture bytes must not carry (a scrubbed
// credential's identifying text).
func promoteAndLoad(t *testing.T, raw *Corpus, filename, service, incumbent, firstCapturedAt string, refs map[string]string, mustNotContain []string) *Corpus {
	t.Helper()
	fixture := &Corpus{
		Schema:      SchemaVersion,
		Service:     service,
		Incumbent:   incumbent,
		CapturedAt:  firstCapturedAt,
		Description: "promoted fixture (promotion lifecycle pin)",
	}
	for _, e := range raw.Entries {
		e.Response = nil
		e.Secrets = nil
		if ref, ok := refs[e.ID]; ok {
			e.Secrets = []Secret{{Ref: ref, InjectAs: InjectAs{Kind: "bearer"}}}
		}
		e.Expect = &Expect{IgnoreHeaders: []string{"Date", "Server", "X-Request-Id"}}
		fixture.Entries = append(fixture.Entries, e)
	}
	path := filepath.Join(t.TempDir(), filename)
	if err := fixture.Save(path); err != nil {
		t.Fatalf("save promoted fixture %s: %v", filename, err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reread promoted fixture %s: %v", filename, err)
	}
	for _, forbidden := range mustNotContain {
		if strings.Contains(string(content), forbidden) {
			t.Errorf("promoted fixture %s still contains %q — the review step must scrub it before the fixture is committed", filename, forbidden)
		}
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("promoted fixture %s failed the fixture gate: %v", filename, err)
	}
	return loaded
}

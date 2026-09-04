package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/shurcooL/githubv4"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// recordingTransport stands in for the GitHub GraphQL endpoint. It answers each
// call with a canned response chosen from the query string and records the
// variables that were sent, so tests can assert on the wire values rather than
// on internal function arguments.
type recordingTransport struct {
	mu       sync.Mutex
	requests []graphqlRequest
}

type graphqlRequest struct {
	query     string
	variables map[string]interface{}
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Query     string                 `json:"query"`
		Variables map[string]interface{} `json:"variables"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}

	t.mu.Lock()
	t.requests = append(t.requests, graphqlRequest{query: payload.Query, variables: payload.Variables})
	t.mu.Unlock()

	resp := map[string]interface{}{"data": t.respond(payload.Query)}
	encoded, err := json.Marshal(resp)
	if err != nil {
		return nil, err
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(encoded)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

// respond returns the data payload for a query, matched on the one field or
// mutation each query in this package selects.
func (t *recordingTransport) respond(query string) map[string]interface{} {
	switch {
	case strings.Contains(query, "createPullRequest"):
		return map[string]interface{}{
			"createPullRequest": map[string]interface{}{
				"pullRequest": map[string]interface{}{
					"id": "PR_ID", "url": "https://github.com/jedarden/declarative-config/pull/7",
					"number": 7, "title": "t", "headRefName": "seam-deprecate-x",
				},
			},
		}
	case strings.Contains(query, "createCommitOnBranch"):
		return map[string]interface{}{"createCommitOnBranch": map[string]interface{}{"commit": map[string]interface{}{"oid": "commitsha"}}}
	case strings.Contains(query, "createRef"):
		return map[string]interface{}{"createRef": map[string]interface{}{"ref": map[string]interface{}{"id": "REF_ID"}}}
	case strings.Contains(query, "addLabelsToLabelable"):
		return map[string]interface{}{"addLabelsToLabelable": map[string]interface{}{"labelable": map[string]interface{}{"id": "PR_ID"}}}
	case strings.Contains(query, "label(name:"):
		return map[string]interface{}{"repository": map[string]interface{}{"label": map[string]interface{}{"id": "LABEL_seam"}}}
	case strings.Contains(query, "defaultBranchRef"):
		return map[string]interface{}{"repository": map[string]interface{}{"defaultBranchRef": map[string]interface{}{"name": "main", "target": map[string]interface{}{"oid": "basesha"}}}}
	case strings.Contains(query, "repository(owner:"):
		return map[string]interface{}{"repository": map[string]interface{}{"id": "REPO_ID"}}
	}
	panic(fmt.Sprintf("no canned response for query: %s", query))
}

func (t *recordingTransport) client() *GitHubClient {
	return &GitHubClient{
		client: githubv4.NewClient(&http.Client{Transport: t}),
		owner:  "jedarden",
		repo:   "declarative-config",
	}
}

// calls returns the recorded variables of every request whose query contains
// marker, in request order.
func (t *recordingTransport) calls(marker string) []graphqlRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []graphqlRequest
	for _, r := range t.requests {
		if strings.Contains(r.query, marker) {
			out = append(out, r)
		}
	}
	return out
}

func TestOpenPRUsesBranchNamesNotNodeIDs(t *testing.T) {
	tr := &recordingTransport{}
	ghc := tr.client()

	url, err := ghc.OpenPR(context.Background(), "seam-deprecate-x", "title", "body",
		[]FileChange{{Path: "k8s/rs-manager/seam/routes.d/x/fragment.yaml", Content: "x-seam-deprecated:\n"}},
		[]string{"seam"})
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	if url != "https://github.com/jedarden/declarative-config/pull/7" {
		t.Errorf("PR url = %q", url)
	}

	// The base ref must be the branch NAME from defaultBranchRef, not the
	// ref's node ID — BaseRefName takes a name and GitHub rejects a node ID.
	prCalls := tr.calls("createPullRequest")
	if len(prCalls) != 1 {
		t.Fatalf("createPullRequest called %d times, want 1", len(prCalls))
	}
	input := prCalls[0].variables["input"].(map[string]interface{})
	if got := input["baseRefName"]; got != "main" {
		t.Errorf("baseRefName = %v (%T), want the branch name %q", got, got, "main")
	}
	if got := input["headRefName"]; got != "seam-deprecate-x" {
		t.Errorf("headRefName = %v, want the plain branch name", got)
	}
}

func TestOpenPRCommitsFilesBeforeOpening(t *testing.T) {
	tr := &recordingTransport{}
	ghc := tr.client()

	_, err := ghc.OpenPR(context.Background(), "seam-deprecate-x", "title", "body",
		[]FileChange{{Path: "k8s/rs-manager/seam/routes.d/x/fragment.yaml", Content: "x-seam-deprecated:\n"}}, nil)
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}

	commits := tr.calls("createCommitOnBranch")
	if len(commits) != 1 {
		t.Fatalf("createCommitOnBranch called %d times, want 1", len(commits))
	}
	branch := commits[0].variables["input"].(map[string]interface{})["branch"].(map[string]interface{})
	if got := branch["branchName"]; got != "seam-deprecate-x" {
		t.Errorf("commit branchName = %v, want the new branch", got)
	}
	if got := branch["repositoryNameWithOwner"]; got != "jedarden/declarative-config" {
		t.Errorf("commit repositoryNameWithOwner = %v", got)
	}

	// The commit must land on the branch as created (the base OID), and the
	// branch must have been created at the base OID in the first place.
	refs := tr.calls("createRef")
	if len(refs) != 1 {
		t.Fatalf("createRef called %d times, want 1", len(refs))
	}
	if got := refs[0].variables["input"].(map[string]interface{})["oid"]; got != "basesha" {
		t.Errorf("createRef oid = %v, want the default branch HEAD", got)
	}
	if got := commits[0].variables["input"].(map[string]interface{})["expectedHeadOid"]; got != "basesha" {
		t.Errorf("expectedHeadOid = %v, want the base OID the branch was created at", got)
	}
}

func TestOpenPRWithoutFileChangesIsRefused(t *testing.T) {
	tr := &recordingTransport{}
	ghc := tr.client()

	_, err := ghc.OpenPR(context.Background(), "seam-deprecate-x", "title", "body", nil, nil)
	if err == nil {
		t.Fatal("OpenPR with no file changes: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "no file changes") {
		t.Errorf("error = %v, want it to name the missing file changes", err)
	}
	// Nothing may have been sent: a zero-diff PR can never succeed.
	if n := len(tr.calls("")); n != 0 {
		t.Errorf("%d GraphQL calls made, want none", n)
	}
}

func TestOpenPRResolvesLabelNamesToNodeIDs(t *testing.T) {
	tr := &recordingTransport{}
	ghc := tr.client()

	if _, err := ghc.OpenPR(context.Background(), "seam-deprecate-x", "title", "body",
		[]FileChange{{Path: "p", Content: "c"}}, []string{"seam"}); err != nil {
		t.Fatalf("OpenPR: %v", err)
	}

	calls := tr.calls("addLabelsToLabelable")
	if len(calls) != 1 {
		t.Fatalf("addLabelsToLabelable called %d times, want 1", len(calls))
	}
	labelIDs := calls[0].variables["input"].(map[string]interface{})["labelIds"].([]interface{})
	if len(labelIDs) != 1 || labelIDs[0] != "LABEL_seam" {
		t.Errorf("labelIds = %v, want the resolved node ID [LABEL_seam]", labelIDs)
	}
}

package main

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/shurcooL/githubv4"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
)

// FileChange is one file the PR branch should carry. Content replaces any
// existing file at Path in full, so it must be the whole file, not a patch.
type FileChange struct {
	Path    string
	Content string
}

// GitHubClient wraps GitHub GraphQL API operations
type GitHubClient struct {
	client *githubv4.Client
	owner  string
	repo   string
	token  string // Token is for reference only; never logged
}

// NewGitHubClient creates a new GitHub client
func NewGitHubClient(token, owner, repo string) *GitHubClient {
	if token == "" {
		zap.L().Fatal("GitHub token is required but was not provided")
	}

	// githubv4.NewClient takes a plain *http.Client; oauth2 supplies the token transport.
	src := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	client := githubv4.NewClient(oauth2.NewClient(context.Background(), src))

	return &GitHubClient{
		client: client,
		owner:  owner,
		repo:   repo,
		token:  token,
	}
}

// OpenPR opens a pull request to the declarative-config repository. files are
// committed to a fresh branch before the PR is opened; at least one is
// required, because GitHub rejects a pull request whose branch carries no
// difference from its base.
func (ghc *GitHubClient) OpenPR(ctx context.Context, branch, title, body string, files []FileChange, labels []string) (string, error) {
	if len(files) == 0 {
		return "", fmt.Errorf("refusing to open a pull request with no file changes: the branch would carry no diff and GitHub rejects it")
	}

	zap.L().Info("Opening GitHub PR",
		zap.String("branch", branch),
		zap.String("title", title),
		zap.String("repo", fmt.Sprintf("%s/%s", ghc.owner, ghc.repo)))

	// First, get the repository ID
	repoID, err := ghc.getRepositoryID(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get repository ID: %w", err)
	}

	// Get the default branch name and its HEAD OID
	baseBranchName, baseBranchOid, err := ghc.getDefaultBranchRef(ctx, repoID)
	if err != nil {
		return "", fmt.Errorf("failed to get default branch: %w", err)
	}

	// Create the branch
	if err := ghc.createBranch(ctx, repoID, branch, baseBranchOid); err != nil {
		return "", fmt.Errorf("failed to create branch: %w", err)
	}

	// The branch still points at the base OID, which is what the commit
	// expects to find at its head.
	if err := ghc.commitFiles(ctx, branch, baseBranchOid, title, files); err != nil {
		return "", fmt.Errorf("failed to commit files to %s: %w", branch, err)
	}

	// Create the PR
	prURL, err := ghc.createPullRequest(ctx, repoID, branch, baseBranchName, title, body, labels)
	if err != nil {
		return "", fmt.Errorf("failed to create pull request: %w", err)
	}

	return prURL, nil
}

// getRepositoryID gets the GraphQL ID of the repository
func (ghc *GitHubClient) getRepositoryID(ctx context.Context) (githubv4.ID, error) {
	var query struct {
		Repository struct {
			ID githubv4.ID `graphql:"id"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	variables := map[string]interface{}{
		"owner": githubv4.String(ghc.owner),
		"name":  githubv4.String(ghc.repo),
	}

	if err := ghc.client.Query(ctx, &query, variables); err != nil {
		return "", fmt.Errorf("failed to query repository: %w", err)
	}

	return query.Repository.ID, nil
}

// getDefaultBranchRef gets the default branch's name and HEAD OID. The branch
// name is what BaseRefName and CommittableBranch want; the ref's node ID is
// deliberately not fetched, because nothing here consumes it and returning it
// invites passing it where a branch name belongs.
func (ghc *GitHubClient) getDefaultBranchRef(ctx context.Context, repoID githubv4.ID) (string, githubv4.GitObjectID, error) {
	var query struct {
		Repository struct {
			DefaultBranchRef struct {
				Name   string `graphql:"name"`
				Target struct {
					Oid githubv4.GitObjectID `graphql:"oid"`
				} `graphql:"target"`
			} `graphql:"defaultBranchRef"`
		} `graphql:"repository(id: $repositoryId)"`
	}

	variables := map[string]interface{}{
		"repositoryId": repoID,
	}

	if err := ghc.client.Query(ctx, &query, variables); err != nil {
		return "", "", fmt.Errorf("failed to query default branch: %w", err)
	}

	return query.Repository.DefaultBranchRef.Name,
		query.Repository.DefaultBranchRef.Target.Oid,
		nil
}

// commitFiles appends a commit carrying files to the top of branch, which must
// already exist and point at headOid. FileChange.Content is sent base64
// encoded, as FileAddition requires.
func (ghc *GitHubClient) commitFiles(ctx context.Context, branch string, headOid githubv4.GitObjectID, headline string, files []FileChange) error {
	additions := make([]githubv4.FileAddition, 0, len(files))
	for _, f := range files {
		additions = append(additions, githubv4.FileAddition{
			Path:     githubv4.String(f.Path),
			Contents: githubv4.Base64String(base64.StdEncoding.EncodeToString([]byte(f.Content))),
		})
	}

	var mutation struct {
		CreateCommitOnBranch struct {
			Commit struct {
				Oid githubv4.GitObjectID `graphql:"oid"`
			} `graphql:"commit"`
		} `graphql:"createCommitOnBranch(input: $input)"`
	}

	input := githubv4.CreateCommitOnBranchInput{
		Branch: githubv4.CommittableBranch{
			RepositoryNameWithOwner: githubv4.NewString(githubv4.String(fmt.Sprintf("%s/%s", ghc.owner, ghc.repo))),
			BranchName:              githubv4.NewString(githubv4.String(branch)),
		},
		Message: githubv4.CommitMessage{
			Headline: githubv4.String(headline),
		},
		ExpectedHeadOid: headOid,
		FileChanges: &githubv4.FileChanges{
			Additions: &additions,
		},
	}

	if err := ghc.client.Mutate(ctx, &mutation, input, nil); err != nil {
		return err
	}

	zap.L().Info("Committed files to branch",
		zap.Int("files", len(files)),
		zap.String("branch", branch),
		zap.String("commit", string(mutation.CreateCommitOnBranch.Commit.Oid)))
	return nil
}

// createBranch creates a new branch pointing at oid
func (ghc *GitHubClient) createBranch(ctx context.Context, repoID githubv4.ID, branch string, oid githubv4.GitObjectID) error {
	var mutation struct {
		CreateRef struct {
			Ref struct {
				ID githubv4.ID `graphql:"id"`
			} `graphql:"ref"`
		} `graphql:"createRef(input: $input)"`
	}

	input := githubv4.CreateRefInput{
		RepositoryID: repoID,
		Name:         githubv4.String("refs/heads/" + branch),
		Oid:          oid,
	}

	if err := ghc.client.Mutate(ctx, &mutation, input, nil); err != nil {
		return fmt.Errorf("failed to create branch: %w", err)
	}

	zap.L().Info("Created branch", zap.String("branch", branch))
	return nil
}

// createPullRequest creates a pull request. branch and baseBranchName are
// branch names, not node IDs: BaseRefName and HeadRefName both take names.
// The branch is created in this repository, so no owner prefix is needed.
func (ghc *GitHubClient) createPullRequest(ctx context.Context, repoID githubv4.ID, branch, baseBranchName, title, body string, labels []string) (string, error) {
	var mutation struct {
		CreatePullRequest struct {
			PullRequest struct {
				ID          githubv4.ID `graphql:"id"`
				URL         string      `graphql:"url"`
				Number      int         `graphql:"number"`
				Title       string      `graphql:"title"`
				HeadRefName string      `graphql:"headRefName"`
			} `graphql:"pullRequest"`
		} `graphql:"createPullRequest(input: $input)"`
	}

	input := githubv4.CreatePullRequestInput{
		RepositoryID: repoID,
		BaseRefName:  githubv4.String(baseBranchName),
		HeadRefName:  githubv4.String(branch),
		Title:        githubv4.String(title),
		Body:         githubv4.NewString(githubv4.String(body)),
	}

	if err := ghc.client.Mutate(ctx, &mutation, input, nil); err != nil {
		return "", fmt.Errorf("failed to create pull request: %w", err)
	}

	// Add labels if provided. AddLabelsToLabelable wants label node IDs, so
	// resolve the names first; an unresolvable label is skipped rather than
	// failing an otherwise-good PR.
	if len(labels) > 0 {
		labelIDs := ghc.resolveLabels(ctx, labels)
		if len(labelIDs) > 0 {
			if err := ghc.addLabelsToPR(ctx, mutation.CreatePullRequest.PullRequest.ID, labelIDs); err != nil {
				zap.L().Warn("Failed to add labels to PR", zap.Error(err))
			}
		}
	}

	zap.L().Info("Created pull request",
		zap.Int("number", mutation.CreatePullRequest.PullRequest.Number),
		zap.String("url", mutation.CreatePullRequest.PullRequest.URL),
		zap.String("branch", branch))

	return mutation.CreatePullRequest.PullRequest.URL, nil
}

// resolveLabels resolves label names to the node IDs addLabelsToLabelable
// requires. Names with no matching label in the repository are dropped with a
// warning.
func (ghc *GitHubClient) resolveLabels(ctx context.Context, names []string) []githubv4.ID {
	ids := make([]githubv4.ID, 0, len(names))
	for _, name := range names {
		id, err := ghc.labelID(ctx, name)
		if err != nil {
			zap.L().Warn("Skipping label that could not be resolved", zap.String("label", name), zap.Error(err))
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// labelID looks up the node ID of a single repository label
func (ghc *GitHubClient) labelID(ctx context.Context, name string) (githubv4.ID, error) {
	var query struct {
		Repository struct {
			Label struct {
				ID githubv4.ID `graphql:"id"`
			} `graphql:"label(name: $labelName)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	variables := map[string]interface{}{
		"owner":     githubv4.String(ghc.owner),
		"name":      githubv4.String(ghc.repo),
		"labelName": githubv4.String(name),
	}

	if err := ghc.client.Query(ctx, &query, variables); err != nil {
		return nil, fmt.Errorf("failed to look up label %q: %w", name, err)
	}

	if query.Repository.Label.ID == nil {
		return nil, fmt.Errorf("label %q does not exist in %s/%s", name, ghc.owner, ghc.repo)
	}

	return query.Repository.Label.ID, nil
}

// addLabelsToPR adds already-resolved labels to a pull request
func (ghc *GitHubClient) addLabelsToPR(ctx context.Context, prID githubv4.ID, labelIDs []githubv4.ID) error {
	var mutation struct {
		AddLabelsToLabelable struct {
			Labelable struct {
				ID githubv4.ID `graphql:"id"`
			} `graphql:"labelable"`
		} `graphql:"addLabelsToLabelable(input: $input)"`
	}

	input := githubv4.AddLabelsToLabelableInput{
		LabelableID: prID,
		LabelIDs:    labelIDs,
	}

	if err := ghc.client.Mutate(ctx, &mutation, input, nil); err != nil {
		return fmt.Errorf("failed to add labels: %w", err)
	}

	return nil
}

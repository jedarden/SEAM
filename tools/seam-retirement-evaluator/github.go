package main

import (
	"context"
	"fmt"

	"github.com/shurcooL/githubv4"
	"go.uber.org/zap"
)

// GitHubClient wraps GitHub GraphQL API operations
type GitHubClient struct {
	client    *githubv4.Client
	owner     string
	repo      string
	token     string // Token is for reference only; never logged
}

// NewGitHubClient creates a new GitHub client
func NewGitHubClient(token, owner, repo string) *GitHubClient {
	if token == "" {
		zap.L().Fatal("GitHub token is required but was not provided")
	}

	client := githubv4.NewClient(githubv4.NewOAuthClient(token, nil))

	return &GitHubClient{
		client: client,
		owner:  owner,
		repo:   repo,
		token:  token,
	}
}

// OpenPR opens a pull request to the declarative-config repository
func (ghc *GitHubClient) OpenPR(ctx context.Context, branch, title, body string, labels []string) (string, error) {
	zap.L().Info("Opening GitHub PR",
		zap.String("branch", branch),
		zap.String("title", title),
		zap.String("repo", fmt.Sprintf("%s/%s", ghc.owner, ghc.repo)))

	// First, get the repository ID
	repoID, err := ghc.getRepositoryID(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get repository ID: %w", err)
	}

	// Get the default branch HEAD OID
	baseRefID, baseBranchOid, err := ghc.getDefaultBranchRef(ctx, repoID)
	if err != nil {
		return "", fmt.Errorf("failed to get default branch: %w", err)
	}

	// Create the branch
	if err := ghc.createBranch(ctx, repoID, branch, baseBranchOid); err != nil {
		return "", fmt.Errorf("failed to create branch: %w", err)
	}

	// Create a commit on the new branch
	// For now, this is a placeholder - in production you'd:
	// 1. Clone the repo
	// 2. Modify the fragment file
	// 3. Push the changes
	// 4. Create the PR

	// Create the PR
	prURL, err := ghc.createPullRequest(ctx, repoID, branch, baseRefID, title, body, labels)
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

// getDefaultBranchRef gets the default branch reference and HEAD OID
func (ghc *GitHubClient) getDefaultBranchRef(ctx context.Context, repoID githubv4.ID) (string, string, error) {
	var query struct {
		Repository struct {
			DefaultBranchRef struct {
				ID  githubv4.ID `graphql:"id"`
				Name string     `graphql:"name"`
				Target struct {
					Oid string `graphql:"oid"`
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

	return string(query.Repository.DefaultBranchRef.ID),
		query.Repository.DefaultBranchRef.Target.Oid,
		nil
}

// createBranch creates a new branch
func (ghc *GitHubClient) createBranch(ctx context.Context, repoID githubv4.ID, branch, oid string) error {
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
		Oid:          githubv4.GitObjectID(oid),
	}

	if err := ghc.client.Mutate(ctx, &mutation, input, nil); err != nil {
		return fmt.Errorf("failed to create branch: %w", err)
	}

	zap.L().Info("Created branch", zap.String("branch", branch))
	return nil
}

// createPullRequest creates a pull request
func (ghc *GitHubClient) createPullRequest(ctx context.Context, repoID githubv4.ID, branch, baseRef, title, body string, labels []string) (string, error) {
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

	headRef := fmt.Sprintf("%s:%s", ghc.owner, branch)

	input := githubv4.CreatePullRequestInput{
		RepositoryID: repoID,
		BaseRefName:  githubv4.String(baseRef),
		HeadRefName:  githubv4.String(headRef),
		Title:        githubv4.String(title),
		Body:         githubv4.String(body),
	}

	if err := ghc.client.Mutate(ctx, &mutation, input, nil); err != nil {
		return "", fmt.Errorf("failed to create pull request: %w", err)
	}

	// Add labels if provided
	if len(labels) > 0 {
		if err := ghc.addLabelsToPR(ctx, mutation.CreatePullRequest.PullRequest.ID, labels); err != nil {
			zap.L().Warn("Failed to add labels to PR", zap.Error(err))
		}
	}

	zap.L().Info("Created pull request",
		zap.Int("number", mutation.CreatePullRequest.PullRequest.Number),
		zap.String("url", mutation.CreatePullRequest.PullRequest.URL),
		zap.String("branch", branch))

	return mutation.CreatePullRequest.PullRequest.URL, nil
}

// addLabelsToPR adds labels to a pull request
func (ghc *GitHubClient) addLabelsToPR(ctx context.Context, prID githubv4.ID, labels []string) error {
	var mutation struct {
		AddLabelsToLabelable struct {
			Labelable struct {
				ID githubv4.ID `graphql:"id"`
			} `graphql:"labelable"`
		} `graphql:"addLabelsToLabelable(input: $input)"`
	}

	labelIDs := make([]githubv4.ID, len(labels))
	for i, label := range labels {
		labelIDs[i] = githubv4.ID(label)
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

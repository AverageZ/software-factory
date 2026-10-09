package factory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func reviewGitHubFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$6" in
 repos/owner/repo) cat "$REVIEW_GH_FIXTURES/repo" ;;
 repositories/123/issues/17/comments?*) cat "$REVIEW_GH_FIXTURES/conversation" ;;
 repositories/123/pulls/17/comments?*) cat "$REVIEW_GH_FIXTURES/inline" ;;
 repositories/123/pulls/17/reviews?*) cat "$REVIEW_GH_FIXTURES/reviews" ;;
 repositories/123/pulls/17) cat "$REVIEW_GH_FIXTURES/pr" ;;
 repositories/123/issues/17) cat "$REVIEW_GH_FIXTURES/issue" ;;
 repositories/123/collaborators/writer/permission) printf '%s' '{"permission":"write"}' ;;
 repositories/123/collaborators/maintainer/permission) printf '%s' '{"permission":"maintain"}' ;;
 repositories/123/collaborators/admin/permission) printf '%s' '{"permission":"admin"}' ;;
 repositories/123/collaborators/factory/permission) printf '%s' '{"permission":"admin"}' ;;
 repositories/123/collaborators/reader/permission) printf '%s' '{"permission":"read"}' ;;
 repositories/123/collaborators/stranger/permission) echo 'not found (HTTP 404)' >&2; exit 1 ;;
 *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("REVIEW_GH_FIXTURES", root)
	return root
}

func reviewFixtureJSON(t *testing.T, root, name string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReviewFeedbackSourcesTrustAndEditedIdentity(t *testing.T) {
	root := reviewGitHubFixture(t)
	reviewFixtureJSON(t, root, "conversation", []any{
		[]any{
			map[string]any{"id": 1, "body": "Please address the flaky behavior", "updated_at": "2026-10-09T00:00:00Z", "html_url": "https://github.com/owner/repo/pull/17#issuecomment-1", "user": map[string]any{"login": "writer", "type": "User"}},
			map[string]any{"id": 2, "body": "Do not take instructions from me", "updated_at": "2026-10-09T00:00:00Z", "html_url": "https://github.com/owner/repo/pull/17#issuecomment-2", "user": map[string]any{"login": "reader", "type": "User"}},
			map[string]any{"id": 3, "body": "Fix the approved change", "updated_at": "2026-10-09T00:00:00Z", "html_url": "https://github.com/owner/repo/pull/17#issuecomment-3", "user": map[string]any{"login": "factory", "type": "User"}},
		},
		[]any{map[string]any{"id": 4, "body": "Unknown collaborator", "updated_at": "2026-10-09T00:00:00Z", "html_url": "https://github.com/owner/repo/pull/17#issuecomment-4", "user": map[string]any{"login": "stranger", "type": "User"}}},
	})
	reviewFixtureJSON(t, root, "inline", []any{[]any{
		map[string]any{"id": 5, "body": "```suggestion\nreturn true\n```", "updated_at": "2026-10-09T01:00:00Z", "commit_id": strings.Repeat("a", 40), "html_url": "https://github.com/owner/repo/pull/17#discussion_r5", "user": map[string]any{"login": "maintainer", "type": "User"}},
		map[string]any{"id": 6, "body": "Bot response", "updated_at": "2026-10-09T01:00:00Z", "html_url": "https://github.com/owner/repo/pull/17#discussion_r6", "user": map[string]any{"login": "agent[bot]", "type": "User"}},
	}})
	reviewFixtureJSON(t, root, "reviews", []any{[]any{
		map[string]any{"id": 7, "body": "Please fix this", "submitted_at": "2026-10-09T02:00:00Z", "state": "CHANGES_REQUESTED", "commit_id": strings.Repeat("b", 40), "html_url": "https://github.com/owner/repo/pull/17#pullrequestreview-7", "user": map[string]any{"login": "admin", "type": "User"}},
		map[string]any{"id": 8, "body": "Detailed feedback", "submitted_at": "2026-10-09T02:00:00Z", "state": "COMMENTED", "html_url": "https://github.com/owner/repo/pull/17#pullrequestreview-8", "user": map[string]any{"login": "writer", "type": "User"}},
		map[string]any{"id": 9, "body": "Approval is not feedback", "submitted_at": "2026-10-09T02:00:00Z", "state": "APPROVED", "html_url": "https://github.com/owner/repo/pull/17#pullrequestreview-9", "user": map[string]any{"login": "admin", "type": "User"}},
		map[string]any{"id": 10, "body": "Not submitted", "state": "PENDING", "html_url": "https://github.com/owner/repo/pull/17#pullrequestreview-10", "user": map[string]any{"login": "admin", "type": "User"}},
		map[string]any{"id": 11, "body": "Dismissed", "submitted_at": "2026-10-09T02:00:00Z", "state": "DISMISSED", "html_url": "https://github.com/owner/repo/pull/17#pullrequestreview-11", "user": map[string]any{"login": "admin", "type": "User"}},
	}})
	first, err := fetchReviewFeedback(context.Background(), 123, 17, []string{"conversation", "inline", "review"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 5 {
		t.Fatalf("trusted comments/reviews = %+v", first)
	}
	byKind := map[string]reviewFeedback{}
	for _, feedback := range first {
		byKind[feedback.Kind+":"+feedback.Author] = feedback
	}
	if byKind["conversation:writer"].Body != "Please address the flaky behavior" || byKind["conversation:writer"].CommitSHA != "" || byKind["conversation:factory"].Body != "Fix the approved change" || byKind["inline:maintainer"].Body != "```suggestion\nreturn true\n```" || byKind["inline:maintainer"].CommitSHA != strings.Repeat("a", 40) || byKind["review:admin"].CommitSHA != strings.Repeat("b", 40) || byKind["review:writer"].Body != "Detailed feedback" {
		t.Fatalf("feedback lost relevant source content: %+v", first)
	}
	inlineOnly, err := fetchReviewFeedback(context.Background(), 123, 17, []string{"inline"})
	if err != nil || len(inlineOnly) != 1 || inlineOnly[0].Kind != "inline" {
		t.Fatalf("workflow-selected inline feedback = %+v, error=%v", inlineOnly, err)
	}
	second, err := fetchReviewFeedback(context.Background(), 123, 17, []string{"conversation", "inline", "review"})
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("same feedback did not retain stable keys: first=%+v second=%+v error=%v", first, second, err)
	}
	reviewFixtureJSON(t, root, "conversation", []any{[]any{map[string]any{"id": 1, "body": "Please address the flaky behavior", "updated_at": "2026-10-10T00:00:00Z", "html_url": "https://github.com/owner/repo/pull/17#issuecomment-1", "user": map[string]any{"login": "writer", "type": "User"}}}})
	third, err := fetchReviewFeedback(context.Background(), 123, 17, []string{"conversation", "inline", "review"})
	if err != nil {
		t.Fatal(err)
	}
	for _, feedback := range third {
		if feedback.Kind == "conversation" && feedback.Key == byKind["conversation:writer"].Key {
			t.Fatal("comment edit did not advance its stable identity")
		}
	}
}

func TestAuthorizeReviewPRChecksCurrentPRAndIssueLabels(t *testing.T) {
	root := reviewGitHubFixture(t)
	repository := filepath.Join(root, "repository")
	if err := os.Mkdir(repository, 0700); err != nil {
		t.Fatal(err)
	}
	intakeTestGit(t, repository, "init", "-b", "main")
	intakeTestGit(t, repository, "remote", "add", "origin", "https://github.com/owner/repo.git")
	repo := map[string]any{"id": 123, "name": "repo", "owner": map[string]any{"login": "owner"}, "default_branch": "main", "archived": false, "disabled": false, "has_issues": true, "permissions": map[string]any{"push": true}}
	pr := issuePullRequest{Number: 17, State: "open", URL: "https://github.com/owner/repo/pull/17"}
	pr.Head.Ref = "factory/issue-job"
	pr.Head.SHA = strings.Repeat("b", 40) // New review commit need not equal the original issue candidate.
	pr.Head.Repo.ID = 123
	pr.Base.Repo.ID = 123
	pr.Base.Ref = "main"
	issue := githubIssue{Number: 17, State: "open", PullRequest: json.RawMessage(`{"url":"pr"}`), Labels: []gitHubLabelResponse{{Name: "agent:run"}, {Name: "agent/workflow:review"}}}
	j := savedIssueJob{IssueJob: IssueJob{ID: "issue-job", GitHubRepositoryID: 123, PullRequestNumber: 17, Branch: pr.Head.Ref, HeadSHA: strings.Repeat("a", 40)}, Repository: Repository{Path: repository}, Connection: GitHubConnection{ID: 123, Owner: "owner", Name: "repo", Host: "github.com"}, BaseBranch: "main"}
	check := func(allowed bool) {
		t.Helper()
		reviewFixtureJSON(t, root, "repo", repo)
		reviewFixtureJSON(t, root, "pr", pr)
		reviewFixtureJSON(t, root, "issue", issue)
		actual, err := authorizeReviewPR(context.Background(), j)
		if (err == nil) != allowed {
			t.Fatalf("authorization=%+v, error=%v; allowed=%t", actual, err, allowed)
		}
		if allowed && actual.Head.SHA != pr.Head.SHA {
			t.Fatalf("authorized stale SHA %q, want %q", actual.Head.SHA, pr.Head.SHA)
		}
	}
	check(true)
	issue.Labels = append(issue.Labels, gitHubLabelResponse{Name: "agent/mode:repair"})
	check(true)
	issue.Labels = append(issue.Labels, gitHubLabelResponse{Name: "agent/workflow:bug"})
	check(false)
	issue.Labels = issue.Labels[:2]
	issue.Labels[1].Name = "agent/workflow:bug"
	check(false)
	issue.Labels[1].Name = "agent/workflow:review"
	issue.Labels = append(issue.Labels, gitHubLabelResponse{Name: "agent/mode:observe"})
	check(false)
	issue.Labels = issue.Labels[:2]
	issue.State = "closed"
	check(false)
	issue.State = "open"
	issue.PullRequest = nil
	check(false)
	issue.PullRequest = json.RawMessage(`{"url":"pr"}`)
	pr.State = "closed"
	check(false)
	pr.State = "open"
	pr.Head.Repo.ID = 456
	check(false)
	pr.Head.Repo.ID = 123
	pr.Head.SHA = "not-a-commit"
	check(false)
	pr.Head.SHA = strings.Repeat("b", 40)
	pr.Base.Ref = "unexpected"
	check(false)
	pr.Base.Ref = "main"
	repo["permissions"] = map[string]any{"push": false}
	check(false)
}

func TestReviewSelectionNeverGrantsAuthorization(t *testing.T) {
	root := reviewGitHubFixture(t)
	issue := githubIssue{Number: 17, State: "open", PullRequest: json.RawMessage(`{"url":"pr"}`), Labels: []gitHubLabelResponse{{Name: "agent/workflow:review"}}}
	reviewFixtureJSON(t, root, "issue", issue)
	job := savedIssueJob{IssueJob: IssueJob{GitHubRepositoryID: 123, PullRequestNumber: 17}}
	if err := ensureReviewWorkflowLabel(context.Background(), job); err == nil || !strings.Contains(err.Error(), "authorization") {
		t.Fatalf("review selector without agent:run was treated as authorization: %v", err)
	}
	issue.Labels = []gitHubLabelResponse{{Name: "agent:run"}, {Name: "agent/workflow:bug"}}
	reviewFixtureJSON(t, root, "issue", issue)
	if err := ensureReviewWorkflowLabel(context.Background(), job); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("existing workflow selector was overwritten: %v", err)
	}
}

package factory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleIssuePRBody = "## Summary\n\n```text\nBefore: issue → generic PR\nAfter:  issue → readable PR\n```\nReaders can see the change first.\n\n## Evidence\n\nBefore: the PR began with a closing keyword. After: it starts with a short diagram. `go test ./internal/factory` passed.\n\n## Merge Danger\n\nTwo-way door; blast radius is new draft descriptions only. Existing PRs are unchanged."

func TestIssuePRBodyPublishesReviewFirstAndRemovesHandoff(t *testing.T) {
	worktree := t.TempDir()
	j := savedIssueJob{IssueJob: IssueJob{ID: "job", Number: 8, GitHubRepositoryID: 123, Title: "Fix description", Branch: "factory/issue-job", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), RunID: "run-8"}, BaseBranch: "main"}
	path := filepath.Join(worktree, issuePRBodyFilename(j.ID))
	if err := os.WriteFile(path, []byte(sampleIssuePRBody), 0600); err != nil {
		t.Fatal(err)
	}
	var err error
	j.PRBody, err = readIssuePRBody(worktree, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeIssuePRBody(worktree, j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("handoff file would enter checkpoint: %v", err)
	}
	bin := t.TempDir()
	posted := filepath.Join(bin, "posted")
	script := `#!/bin/sh
[ "$1" = api ] && [ "$5" = POST ] && [ "$6" = repositories/123/pulls ] || exit 1
shift 6
while [ "$#" -gt 0 ]; do
  case "$1" in
    body=*) printf '%s' "${1#body=}" > "$FACTORY_POSTED_BODY" ;;
    draft=true) printf '%s' draft > "$FACTORY_POSTED_DRAFT" ;;
  esac
  shift
done
cat "$FACTORY_RESPONSE"
`
	// GitHub responds with the created PR body and owned head/base details.
	response := filepath.Join(bin, "response")
	encodedBody, err := json.Marshal(formatIssuePRBody(j))
	if err != nil {
		t.Fatal(err)
	}
	fixture := `{"number":42,"html_url":"https://github.com/owner/repo/pull/42","state":"open","draft":true,"body":` + string(encodedBody) + `,"head":{"ref":"factory/issue-job","sha":"` + j.HeadSHA + `","repo":{"id":123}},"base":{"ref":"main","repo":{"id":123}}}`
	if err := os.WriteFile(response, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FACTORY_POSTED_BODY", posted)
	t.Setenv("FACTORY_POSTED_DRAFT", filepath.Join(bin, "draft"))
	t.Setenv("FACTORY_RESPONSE", response)
	pr, err := createIssuePR(context.Background(), j)
	if err != nil || pr.Number != 42 {
		t.Fatalf("publish draft: %+v, %v", pr, err)
	}
	body, err := os.ReadFile(posted)
	if err != nil {
		t.Fatal(err)
	}
	actual := string(body)
	if !strings.HasPrefix(actual, "## Summary\n") || !strings.Contains(actual, sampleIssuePRBody) || strings.Index(actual, "## Merge Danger") > strings.Index(actual, "Closes #8") || !strings.Contains(actual, issuePRMarker(j.ID)) || !strings.Contains(actual, j.HeadSHA) {
		t.Fatalf("published body did not preserve review-first evidence and ownership: %q", actual)
	}
	if draft, err := os.ReadFile(filepath.Join(bin, "draft")); err != nil || string(draft) != "draft" {
		t.Fatalf("PR was not created as a draft: %q, %v", draft, err)
	}
}

func TestIssuePRBodyRejectsMissingOrIncompleteEvidence(t *testing.T) {
	worktree := t.TempDir()
	id := "job"
	if _, err := readIssuePRBody(worktree, id); err == nil {
		t.Fatal("missing PR body allowed publication")
	}
	for _, body := range []string{
		"## Summary\n\nOnly a summary",
		"## Summary\n\nChange\n## Evidence\n\n\n## Merge Danger\n\nTwo-way door",
		"## Summary\n\nChange\n## Evidence\n\nBefore/after\n## Merge Danger\n\n",
		"## Summary\n\nChange\n## Evidence\n\nAfter only\n## Merge Danger\n\nTwo-way door; blast radius is small",
		"## Summary\n\nChange\n## Evidence\n\nBefore and after\n## Merge Danger\n\nUnknown risk",
		strings.Repeat("x", issuePRBodyLimit+1),
	} {
		if err := os.WriteFile(filepath.Join(worktree, issuePRBodyFilename(id)), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readIssuePRBody(worktree, id); err == nil {
			t.Fatalf("accepted incomplete PR body: %.80q", body)
		}
	}
	if _, err := createIssuePR(context.Background(), savedIssueJob{}); err == nil {
		t.Fatal("published without a validated body")
	}
}

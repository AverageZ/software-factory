package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

type githubIssue struct {
	Number      int                   `json:"number"`
	Title       string                `json:"title"`
	Body        string                `json:"body"`
	URL         string                `json:"html_url"`
	State       string                `json:"state"`
	Labels      []gitHubLabelResponse `json:"labels"`
	PullRequest json.RawMessage       `json:"pull_request"`
}

// Authorization is independent of projected status/phase labels. An ordinary
// labeled issue selects repair; unsupported or conflicting selectors fail closed.
func issueAuthorization(issue githubIssue) error {
	if len(issue.PullRequest) != 0 && string(issue.PullRequest) != "null" {
		return fmt.Errorf("pull requests are not issue intake subjects")
	}
	if issue.State != "open" {
		return fmt.Errorf("issue is not open")
	}
	authorized := false
	workflows, modes := []string{}, []string{}
	for _, label := range issue.Labels {
		label.Name = strings.ToLower(label.Name)
		switch {
		case label.Name == "agent:run":
			authorized = true
		case strings.HasPrefix(label.Name, "agent/workflow:"):
			workflows = append(workflows, label.Name)
		case strings.HasPrefix(label.Name, "agent/mode:"):
			modes = append(modes, label.Name)
		}
	}
	if !authorized {
		return fmt.Errorf("agent:run authorization is absent")
	}
	if len(workflows) > 1 || (len(workflows) == 1 && workflows[0] != "agent/workflow:bug") {
		return fmt.Errorf("unsupported or conflicting workflow labels; issue intake supports the default repair workflow or agent/workflow:bug")
	}
	if len(modes) > 1 || (len(modes) == 1 && modes[0] != "agent/mode:repair") {
		return fmt.Errorf("unsupported or conflicting mode labels; issue publication requires default repair mode or agent/mode:repair")
	}
	return nil
}
func fetchIntakeIssues(ctx context.Context, repositoryID int64) ([]githubIssue, error) {
	data, err := gitHubAPI(ctx, "GET", fmt.Sprintf("repositories/%d/issues?state=open&labels=agent%%3Arun&per_page=100", repositoryID), "--paginate", "--slurp")
	if err != nil {
		return nil, err
	}
	var pages [][]githubIssue
	if err = json.Unmarshal(data, &pages); err != nil {
		return nil, fmt.Errorf("invalid GitHub issue list: %w", err)
	}
	issues := []githubIssue{}
	for _, page := range pages {
		for _, issue := range page {
			if len(issue.PullRequest) == 0 || string(issue.PullRequest) == "null" {
				issues = append(issues, issue)
			}
		}
	}
	return issues, nil
}
func fetchIntakeIssue(ctx context.Context, repositoryID int64, number int) (githubIssue, error) {
	data, err := gitHubAPI(ctx, "GET", fmt.Sprintf("repositories/%d/issues/%d", repositoryID, number))
	var issue githubIssue
	if err == nil {
		err = json.Unmarshal(data, &issue)
	}
	if err == nil && issue.Number != number {
		err = fmt.Errorf("GitHub issue identity changed")
	}
	return issue, err
}

// Resolve both the configured origin and the immutable API ID before any write.
// Pushes use this canonical HTTPS URL, never origin's possibly unrelated pushurl.
func verifyIssueRepository(ctx context.Context, repositoryPath string, expected GitHubConnection) (GitHubConnection, error) {
	remote, err := discoverGitHubOrigin(ctx, repositoryPath)
	if err != nil {
		return GitHubConnection{}, err
	}
	data, err := gitHubAPI(ctx, "GET", "repos/"+remote.Owner+"/"+remote.Name)
	if err != nil {
		return GitHubConnection{}, err
	}
	var repo gitHubRepositoryResponse
	if err = json.Unmarshal(data, &repo); err != nil {
		return GitHubConnection{}, err
	}
	if repo.ID != expected.ID || repo.Archived == nil || *repo.Archived || repo.Disabled == nil || *repo.Disabled || repo.HasIssues == nil || !*repo.HasIssues || repo.Permissions == nil || !repo.Permissions.Push {
		return GitHubConnection{}, fmt.Errorf("GitHub repository identity, availability or push permission changed; check the connection again")
	}
	if !validGitHubPathPart(repo.Owner.Login, false) || !validGitHubPathPart(repo.Name, true) || repo.DefaultBranch == "" {
		return GitHubConnection{}, fmt.Errorf("invalid GitHub repository response")
	}
	expected.Owner, expected.Name, expected.DefaultBranch = repo.Owner.Login, repo.Name, repo.DefaultBranch
	expected.URL = "https://github.com/" + repo.Owner.Login + "/" + repo.Name
	return expected, nil
}
func issueRemoteURL(c GitHubConnection) string {
	return "https://github.com/" + c.Owner + "/" + c.Name + ".git"
}
func fetchIssueBaseSHA(ctx context.Context, c GitHubConnection, branch string) (string, error) {
	data, err := gitHubAPI(ctx, "GET", fmt.Sprintf("repositories/%d/commits/%s", c.ID, url.PathEscape(branch)))
	if err != nil {
		return "", err
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	if err = json.Unmarshal(data, &commit); err != nil {
		return "", err
	}
	if len(commit.SHA) != 40 && len(commit.SHA) != 64 {
		return "", fmt.Errorf("invalid base commit response")
	}
	return commit.SHA, nil
}

type issuePullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	Body   string `json:"body"`
	Head   struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			ID int64 `json:"id"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref  string `json:"ref"`
		Repo struct {
			ID int64 `json:"id"`
		} `json:"repo"`
	} `json:"base"`
}

func issuePRMarker(id string) string { return "<!-- factory-issue-job:" + id + " -->" }
func validateIssuePR(pr issuePullRequest, j savedIssueJob) error {
	if pr.Number <= 0 || pr.Head.Ref != j.Branch || pr.Head.Repo.ID != j.GitHubRepositoryID || pr.Base.Repo.ID != j.GitHubRepositoryID {
		return fmt.Errorf("pull request does not match Factory ownership; retaining workspace")
	}
	if j.PullRequestNumber > 0 {
		if pr.Number != j.PullRequestNumber {
			return fmt.Errorf("recorded pull request identity changed")
		}
		// Once publication was durably confirmed, human PR edits do not erase
		// that association or prevent safe cleanup of our local checkpoint.
		return nil
	}
	if !strings.Contains(pr.Body, issuePRMarker(j.ID)) || pr.Head.SHA != j.HeadSHA || pr.Base.Ref != j.BaseBranch || (pr.State == "open" && !pr.Draft) {
		return fmt.Errorf("unconfirmed pull request no longer matches the draft checkpoint and base; retaining workspace")
	}
	return nil
}
func findIssuePR(ctx context.Context, j savedIssueJob) (*issuePullRequest, error) {
	endpoint := fmt.Sprintf("repositories/%d/pulls?state=all&head=%s&per_page=100", j.GitHubRepositoryID, url.QueryEscape(j.Connection.Owner+":"+j.Branch))
	if j.PullRequestNumber > 0 {
		endpoint = fmt.Sprintf("repositories/%d/pulls/%d", j.GitHubRepositoryID, j.PullRequestNumber)
	}
	options := []string{}
	if j.PullRequestNumber == 0 {
		options = []string{"--paginate", "--slurp"}
	}
	data, err := gitHubAPI(ctx, "GET", endpoint, options...)
	if err != nil {
		return nil, err
	}
	var prs []issuePullRequest
	if j.PullRequestNumber > 0 {
		var pr issuePullRequest
		err = json.Unmarshal(data, &pr)
		prs = append(prs, pr)
	} else {
		var pages [][]issuePullRequest
		err = json.Unmarshal(data, &pages)
		for _, page := range pages {
			prs = append(prs, page...)
		}
	}
	if err != nil {
		return nil, err
	}
	if len(prs) == 0 {
		return nil, nil
	}
	if len(prs) != 1 {
		return nil, fmt.Errorf("multiple pull requests use the owned branch; manual reconciliation required")
	}
	if err = validateIssuePR(prs[0], j); err != nil {
		return nil, err
	}
	return &prs[0], nil
}
func createIssuePR(ctx context.Context, j savedIssueJob) (issuePullRequest, error) {
	if j.PRBody == "" {
		return issuePullRequest{}, fmt.Errorf("validated PR body is missing; draft publication blocked")
	}
	body := formatIssuePRBody(j)
	data, err := gitHubAPI(ctx, "POST", fmt.Sprintf("repositories/%d/pulls", j.GitHubRepositoryID), "-f", "title="+j.Title, "-f", "head="+j.Branch, "-f", "base="+j.BaseBranch, "-f", "body="+body, "-F", "draft=true")
	var pr issuePullRequest
	if err == nil {
		err = json.Unmarshal(data, &pr)
	}
	if err == nil {
		err = validateIssuePR(pr, j)
	}
	if err == nil && (!pr.Draft || pr.Head.SHA != j.HeadSHA || pr.Base.Ref != j.BaseBranch) {
		err = fmt.Errorf("created pull request does not match the draft candidate")
	}
	return pr, err
}

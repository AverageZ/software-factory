package factory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

type reviewFeedback struct {
	Key       string
	Author    string
	Body      string
	URL       string
	Kind      string
	CommitSHA string
}

type githubReviewEvent struct {
	ID          int64  `json:"id"`
	Body        string `json:"body"`
	URL         string `json:"html_url"`
	UpdatedAt   string `json:"updated_at"`
	SubmittedAt string `json:"submitted_at"`
	CommitSHA   string `json:"commit_id"`
	State       string `json:"state"`
	User        struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
}

// Each edited body gets a new identity even if GitHub does not expose an
// updated_at timestamp (notably for submitted reviews).
func reviewFeedbackKey(kind string, event githubReviewEvent) string {
	stamp := event.UpdatedAt
	if stamp == "" {
		stamp = event.SubmittedAt
	}
	digest := sha256.Sum256([]byte(event.Body))
	return fmt.Sprintf("%s:%d:%s:%x", kind, event.ID, stamp, digest)
}

func fetchReviewFeedback(ctx context.Context, repoID int64, prNumber int, sources []string) ([]reviewFeedback, error) {
	if repoID <= 0 || prNumber <= 0 {
		return nil, fmt.Errorf("invalid pull request identity")
	}
	permission := map[string]bool{}
	seen := map[string]bool{}
	feedback := []reviewFeedback{}
	for _, kind := range sources {
		endpoint := ""
		switch kind {
		case "conversation":
			endpoint = fmt.Sprintf("repositories/%d/issues/%d/comments?per_page=100", repoID, prNumber)
		case "inline":
			endpoint = fmt.Sprintf("repositories/%d/pulls/%d/comments?per_page=100", repoID, prNumber)
		case "review":
			endpoint = fmt.Sprintf("repositories/%d/pulls/%d/reviews?per_page=100", repoID, prNumber)
		default:
			return nil, fmt.Errorf("unsupported review feedback source %q", kind)
		}
		data, err := gitHubAPI(ctx, "GET", endpoint, "--paginate", "--slurp")
		if err != nil {
			return nil, err
		}
		var pages [][]githubReviewEvent
		if err = json.Unmarshal(data, &pages); err != nil {
			return nil, fmt.Errorf("invalid GitHub %s feedback response: %w", kind, err)
		}
		for _, page := range pages {
			for _, event := range page {
				if event.ID <= 0 || event.Body == "" || event.URL == "" || event.User.Login == "" {
					continue
				}
				if kind == "review" && (event.SubmittedAt == "" || (event.State != "CHANGES_REQUESTED" && event.State != "COMMENTED")) {
					continue
				}
				if event.UpdatedAt == "" && event.SubmittedAt == "" {
					continue
				}
				login := strings.ToLower(event.User.Login)
				if strings.EqualFold(event.User.Type, "Bot") || strings.HasSuffix(login, "[bot]") {
					continue
				}
				allowed, checked := permission[login]
				if !checked {
					if !validGitHubPathPart(event.User.Login, false) {
						continue
					}
					response, permissionErr := gitHubAPI(ctx, "GET", fmt.Sprintf("repositories/%d/collaborators/%s/permission", repoID, url.PathEscape(event.User.Login)))
					if permissionErr != nil {
						// A missing collaborator is untrusted. Any other failure prevents
						// us from establishing an accurate trusted feedback snapshot.
						if failure, ok := permissionErr.(*gitHubCommandError); ok && failure.httpStatus == 404 {
							permission[login] = false
							continue
						}
						return nil, permissionErr
					}
					var access struct {
						Permission string `json:"permission"`
					}
					if err = json.Unmarshal(response, &access); err != nil {
						return nil, fmt.Errorf("invalid GitHub collaborator permission response: %w", err)
					}
					allowed = access.Permission == "write" || access.Permission == "maintain" || access.Permission == "admin"
					permission[login] = allowed
				}
				if !allowed {
					continue
				}
				key := reviewFeedbackKey(kind, event)
				if seen[key] {
					continue
				}
				seen[key] = true
				feedback = append(feedback, reviewFeedback{Key: key, Author: event.User.Login, Body: event.Body, URL: event.URL, Kind: kind, CommitSHA: event.CommitSHA})
			}
		}
	}
	sort.Slice(feedback, func(i, j int) bool { return feedback[i].Key < feedback[j].Key })
	return feedback, nil
}

func authorizeReviewPR(ctx context.Context, job savedIssueJob) (issuePullRequest, error) {
	return inspectManagedPR(ctx, job, "review")
}

func authorizeSyncPR(ctx context.Context, job savedIssueJob) (issuePullRequest, error) {
	return inspectManagedPR(ctx, job, "sync")
}

func inspectReviewPR(ctx context.Context, job savedIssueJob, requireReview bool) (issuePullRequest, error) {
	required := ""
	if requireReview {
		required = "review"
	}
	return inspectManagedPR(ctx, job, required)
}

func inspectManagedPR(ctx context.Context, job savedIssueJob, requiredWorkflow string) (issuePullRequest, error) {
	if job.GitHubRepositoryID <= 0 || job.Connection.ID != job.GitHubRepositoryID || job.PullRequestNumber <= 0 || job.Branch == "" {
		return issuePullRequest{}, fmt.Errorf("missing recorded Factory pull request ownership")
	}
	if _, err := verifyIssueRepository(ctx, job.Repository.Path, job.Connection); err != nil {
		return issuePullRequest{}, err
	}
	pr, err := findIssuePR(ctx, job)
	if err != nil {
		return issuePullRequest{}, err
	}
	if pr == nil || pr.State != "open" || !validReviewHeadSHA(pr.Head.SHA) || pr.Base.Ref != job.BaseBranch {
		return issuePullRequest{}, fmt.Errorf("Factory pull request is not open on its recorded base with a valid head commit")
	}
	issue, err := fetchIntakeIssue(ctx, job.GitHubRepositoryID, job.PullRequestNumber)
	if err != nil {
		return issuePullRequest{}, err
	}
	if _, err := prAutomationLabels(issue, requiredWorkflow); err != nil {
		return issuePullRequest{}, err
	}
	return *pr, nil
}

type prAutomationSelection struct {
	Review bool
	Sync   bool
}

func prAutomationLabels(issue githubIssue, requiredWorkflow string) (prAutomationSelection, error) {
	var selection prAutomationSelection
	if len(issue.PullRequest) == 0 || string(issue.PullRequest) == "null" || issue.State != "open" {
		return selection, fmt.Errorf("Factory pull request issue is not open")
	}
	run, modes := false, 0
	for _, label := range issue.Labels {
		name := strings.ToLower(label.Name)
		switch {
		case name == "agent:run":
			run = true
		case name == "agent/workflow:review":
			selection.Review = true
		case name == "agent/workflow:sync":
			selection.Sync = true
		case strings.HasPrefix(name, "agent/workflow:"):
			return selection, fmt.Errorf("conflicting pull request workflow labels")
		case strings.HasPrefix(name, "agent/mode:"):
			modes++
			if name != "agent/mode:repair" {
				return selection, fmt.Errorf("unsupported pull request automation mode")
			}
		}
	}
	selected := requiredWorkflow == "" || requiredWorkflow == "review" && selection.Review || requiredWorkflow == "sync" && selection.Sync
	if !run || !selected || modes > 1 {
		return selection, fmt.Errorf("pull request automation authorization is absent or conflicting")
	}
	return selection, nil
}

func reviewLabels(issue githubIssue, requireReview bool) (bool, error) {
	required := ""
	if requireReview {
		required = "review"
	}
	selection, err := prAutomationLabels(issue, required)
	return selection.Review, err
}

func syncLabels(issue githubIssue, requireSync bool) (bool, error) {
	required := ""
	if requireSync {
		required = "sync"
	}
	selection, err := prAutomationLabels(issue, required)
	return selection.Sync, err
}

// A configured managed workflow selects its own marker. Only agent:run can
// authorize code changes. Never add a selector to an unowned or unauthorized PR.
func ensureReviewWorkflowLabel(ctx context.Context, job savedIssueJob) error {
	return ensurePRWorkflowLabel(ctx, job, "review")
}

func ensureSyncWorkflowLabel(ctx context.Context, job savedIssueJob) error {
	return ensurePRWorkflowLabel(ctx, job, "sync")
}

func ensurePRWorkflowLabel(ctx context.Context, job savedIssueJob, workflow string) error {
	issue, err := fetchIntakeIssue(ctx, job.GitHubRepositoryID, job.PullRequestNumber)
	if err != nil {
		return err
	}
	selection, err := prAutomationLabels(issue, "")
	if err != nil {
		return err
	}
	if workflow == "review" && selection.Review || workflow == "sync" && selection.Sync {
		return nil
	}
	label := "agent/workflow:" + workflow
	data, err := gitHubAPI(ctx, "POST", fmt.Sprintf("repositories/%d/issues/%d/labels", job.GitHubRepositoryID, job.PullRequestNumber), "-f", "labels[]="+label)
	if err != nil {
		return err
	}
	var labels []gitHubLabelResponse
	if err := json.Unmarshal(data, &labels); err != nil {
		return fmt.Errorf("invalid GitHub %s label response: %w", workflow, err)
	}
	for _, observed := range labels {
		if strings.EqualFold(observed.Name, label) {
			return nil
		}
	}
	return fmt.Errorf("GitHub did not confirm the %s workflow label", workflow)
}

func validReviewHeadSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

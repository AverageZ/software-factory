package factory

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
)

type intakeConfig struct {
	Enabled         bool             `json:"enabled"`
	ProjectID       string           `json:"projectId"`
	ReviewBindingID string           `json:"reviewBindingId,omitempty"`
	ReviewDisabled  bool             `json:"reviewDisabled,omitempty"`
	RepositoryID    string           `json:"repositoryId"`
	RepositoryPath  string           `json:"repositoryPath"`
	Connection      GitHubConnection `json:"connection"`
	CheckedAt       string           `json:"checkedAt"`
	Error           string           `json:"error,omitempty"`
}
type IssueJob struct {
	ID                 string `json:"id"`
	ProjectID          string `json:"projectId"`
	RepositoryID       string `json:"repositoryId"`
	GitHubRepositoryID int64  `json:"githubRepositoryId"`
	Number             int    `json:"number"`
	Title              string `json:"title"`
	URL                string `json:"url"`
	Status             string `json:"status"`
	Error              string `json:"error,omitempty"`
	RunID              string `json:"runId,omitempty"`
	Branch             string `json:"branch,omitempty"`
	Worktree           string `json:"worktree,omitempty"`
	BaseSHA            string `json:"baseSHA,omitempty"`
	HeadSHA            string `json:"headSHA,omitempty"`
	PullRequestNumber  int    `json:"pullRequestNumber,omitempty"`
	PullRequestURL     string `json:"pullRequestUrl,omitempty"`
	UpdatedAt          string `json:"updatedAt"`
}
type savedIssueJob struct {
	IssueJob
	Project    Project          `json:"project"`
	Repository Repository       `json:"repository"`
	Connection GitHubConnection `json:"connection"`
	Body       string           `json:"body"`
	PRBody     string           `json:"prBody,omitempty"`
	BaseBranch string           `json:"baseBranch"`
}
type IntakeReport struct {
	Enabled         bool        `json:"enabled"`
	CheckedAt       string      `json:"checkedAt"`
	Error           string      `json:"error,omitempty"`
	Jobs            []IssueJob  `json:"jobs"`
	ReviewBindingID string      `json:"reviewBindingId,omitempty"`
	Revisions       []ReviewJob `json:"revisions"`
}

func (e *Engine) intakeReportLocked(projectID, repositoryID string) IntakeReport {
	config := e.intakes[githubKey(projectID, repositoryID)]
	report := IntakeReport{Enabled: config.Enabled, ReviewBindingID: config.ReviewBindingID, CheckedAt: config.CheckedAt, Error: config.Error, Jobs: []IssueJob{}, Revisions: []ReviewJob{}}
	for _, j := range e.issueJobs {
		if j.ProjectID == projectID && j.RepositoryID == repositoryID {
			report.Jobs = append(report.Jobs, j.IssueJob)
		}
	}
	for _, j := range e.reviewJobs {
		if j.ProjectID == projectID && j.RepositoryID == repositoryID {
			report.Revisions = append(report.Revisions, j.ReviewJob)
		}
	}
	sort.Slice(report.Revisions, func(i, j int) bool { return report.Revisions[i].UpdatedAt > report.Revisions[j].UpdatedAt })
	sort.Slice(report.Jobs, func(i, j int) bool { return report.Jobs[i].UpdatedAt > report.Jobs[j].UpdatedAt })
	return report
}
func (e *Engine) repositoryIntake(projectID, repositoryID string) (IntakeReport, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.repositoryLocked(projectID, repositoryID); err != nil {
		return IntakeReport{}, err
	}
	return e.intakeReportLocked(projectID, repositoryID), nil
}
func (e *Engine) setRepositoryIntake(projectID, repositoryID string, enabled bool) (IntakeReport, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.healthyLocked(); err != nil {
		return IntakeReport{}, err
	}
	repository, err := e.repositoryLocked(projectID, repositoryID)
	if err != nil {
		return IntakeReport{}, err
	}
	key := githubKey(projectID, repositoryID)
	config := e.intakes[key]
	if enabled {
		health := e.github[key]
		if health.RepositoryPath != repository.Path || health.Connection == nil || !health.Connection.CanPush || health.Connection.Archived || !health.Connection.IssuesEnabled {
			return IntakeReport{}, conflict("check the GitHub connection with push permission before enabling intake")
		}
		for other, c := range e.intakes {
			if other != key && c.Enabled && c.Connection.ID == health.Connection.ID {
				return IntakeReport{}, conflict("issue intake is already enabled for this GitHub repository in another attachment")
			}
		}
		config.Connection = *health.Connection
	}
	config.Enabled = enabled
	config.ProjectID, config.RepositoryID, config.RepositoryPath = projectID, repositoryID, repository.Path
	if enabled && config.ReviewBindingID == "" && !config.ReviewDisabled {
		config, err = e.ensureDefaultReviewBindingLocked(config)
		if err != nil {
			e.fatalLocked(err)
			return IntakeReport{}, err
		}
	} else {
		if err = e.store.save(record{"intake", key, config}); err != nil {
			e.fatalLocked(err)
			return IntakeReport{}, err
		}
		e.intakes[key] = config
	}
	select {
	case e.intakeWake <- struct{}{}:
	default:
	}
	return e.intakeReportLocked(projectID, repositoryID), nil
}
func (e *Engine) setReviewBinding(projectID, repositoryID, bindingID string) (IntakeReport, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.healthyLocked(); err != nil {
		return IntakeReport{}, err
	}
	if _, err := e.repositoryLocked(projectID, repositoryID); err != nil {
		return IntakeReport{}, err
	}
	if bindingID != "" {
		if err := e.checkReviewBindingLocked(projectID, repositoryID, bindingID); err != nil {
			return IntakeReport{}, invalid(err)
		}
	}
	key := githubKey(projectID, repositoryID)
	config := e.intakes[key]
	config.ReviewBindingID = bindingID
	config.ReviewDisabled = bindingID == ""
	config.ProjectID, config.RepositoryID = projectID, repositoryID
	if err := e.store.save(record{"intake", key, config}); err != nil {
		e.fatalLocked(err)
		return IntakeReport{}, err
	}
	e.intakes[key] = config
	select {
	case e.intakeWake <- struct{}{}:
	default:
	}
	return e.intakeReportLocked(projectID, repositoryID), nil
}

func (e *Engine) checkIntakeProjectUpdateLocked(p Project) error {
	for _, config := range e.intakes {
		if config.ProjectID != p.ID {
			continue
		}
		retained := config.Enabled
		for _, j := range e.issueJobs {
			if j.ProjectID == p.ID && j.RepositoryID == config.RepositoryID && (j.Worktree != "" || j.Status != "closed") {
				retained = true
				break
			}
		}
		for _, j := range e.reviewJobs {
			if j.ProjectID == p.ID && j.RepositoryID == config.RepositoryID && (j.Worktree != "" || j.Status != "published") {
				retained = true
				break
			}
		}
		if !retained {
			continue
		}
		found := false
		for _, repo := range p.Repositories {
			if repo.ID == config.RepositoryID && repo.Path == config.RepositoryPath {
				found = true
				break
			}
		}
		if !found {
			return conflict("disable intake and resolve retained issue jobs before removing or moving its repository")
		}
	}
	return nil
}
func (e *Engine) saveIssueJobLocked(j savedIssueJob) error {
	j.UpdatedAt = now()
	if err := e.store.save(record{"issue-job", j.ID, j}); err != nil {
		e.fatalLocked(err)
		return err
	}
	e.issueJobs[j.ID] = j
	return nil
}
func (e *Engine) saveIssueJob(j savedIssueJob) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.saveIssueJobLocked(j)
}
func (e *Engine) intakeLoop() {
	defer e.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		e.mu.Lock()
		configs := valuesSorted(e.intakes)
		e.mu.Unlock()
		for _, config := range configs {
			if e.ctx.Err() != nil {
				return
			}
			_, _ = e.pollRepositoryIntake(e.ctx, config.ProjectID, config.RepositoryID)
		}
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
		case <-e.intakeWake:
		}
	}
}
func (e *Engine) pollRepositoryIntake(ctx context.Context, projectID, repositoryID string) (IntakeReport, error) {
	key := githubKey(projectID, repositoryID)
	e.mu.Lock()
	if err := e.healthyLocked(); err != nil {
		e.mu.Unlock()
		return IntakeReport{}, err
	}
	if _, err := e.repositoryLocked(projectID, repositoryID); err != nil {
		e.mu.Unlock()
		return IntakeReport{}, err
	}
	if e.intakeBusy[key] {
		e.mu.Unlock()
		return IntakeReport{}, conflict("issue intake is already polling this repository")
	}
	config, exists := e.intakes[key]
	if !exists {
		report := e.intakeReportLocked(projectID, repositoryID)
		e.mu.Unlock()
		return report, nil
	}
	e.intakeBusy[key] = true
	e.wg.Add(1)
	e.mu.Unlock()
	defer e.wg.Done()
	defer func() { e.mu.Lock(); delete(e.intakeBusy, key); e.mu.Unlock() }()
	operation, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	stop := context.AfterFunc(e.ctx, cancel)
	defer stop()
	var pollErr error
	if config.Enabled {
		var connection GitHubConnection
		connection, pollErr = verifyIssueRepository(operation, config.RepositoryPath, config.Connection)
		if pollErr == nil {
			var issues []githubIssue
			issues, pollErr = fetchIntakeIssues(operation, connection.ID)
			if pollErr == nil {
				e.mu.Lock()
				// A disable during the network request must prevent admission.
				if e.intakes[key].Enabled {
					for _, issue := range issues {
						duplicate := false
						for _, j := range e.issueJobs {
							if j.GitHubRepositoryID == connection.ID && j.Number == issue.Number {
								duplicate = true
								break
							}
						}
						if duplicate {
							continue
						}
						p := copyJSON(e.projects[projectID])
						repo, _ := e.repositoryLocked(projectID, repositoryID)
						j := savedIssueJob{IssueJob: IssueJob{ID: uuid.NewString(), ProjectID: projectID, RepositoryID: repositoryID, GitHubRepositoryID: connection.ID, Number: issue.Number, Title: issue.Title, URL: issue.URL, Status: "queued"}, Project: p, Repository: repo, Connection: connection, Body: issue.Body, BaseBranch: connection.DefaultBranch}
						if err := issueAuthorization(issue); err != nil {
							j.Status = "paused"
							j.Error = err.Error()
						}
						if err := e.saveIssueJobLocked(j); err != nil {
							pollErr = err
							break
						}
					}
				}
				e.mu.Unlock()
			}
		}
	}
	// PR observation and safe local cleanup remain enabled after intake is disabled
	// or issue authorization is removed. Neither action writes to GitHub.
	e.mu.Lock()
	jobs := valuesSorted(e.issueJobs)
	e.mu.Unlock()
	for _, j := range jobs {
		if j.ProjectID != projectID || j.RepositoryID != repositoryID {
			continue
		}
		e.mu.Lock()
		active := e.issueActive[j.ID]
		e.mu.Unlock()
		if active {
			continue
		}
		if j.Branch != "" && (j.HeadSHA != "" || j.PullRequestNumber > 0) {
			if err := e.reconcileIssuePR(operation, j); err != nil && pollErr == nil {
				pollErr = err
			}
		}
	}
	if pollErr == nil && config.Enabled && config.ReviewBindingID != "" {
		if err := e.pollReviewJobs(operation, config); err != nil {
			pollErr = err
		}
	}
	e.mu.Lock()
	latest := e.intakes[key]
	latest.CheckedAt = now()
	latest.Error = ""
	if pollErr != nil {
		latest.Error = pollErr.Error()
	}
	if err := e.store.save(record{"intake", key, latest}); err != nil {
		e.fatalLocked(err)
		e.mu.Unlock()
		return IntakeReport{}, err
	}
	e.intakes[key] = latest
	if pollErr == nil && latest.Enabled {
		e.dispatchIssueJobsLocked(key)
	}
	report := e.intakeReportLocked(projectID, repositoryID)
	e.mu.Unlock()
	return report, nil
}
func (e *Engine) dispatchIssueJobsLocked(key string) {
	jobs := valuesSorted(e.issueJobs)
	for _, j := range jobs {
		if githubKey(j.ProjectID, j.RepositoryID) != key || e.issueActive[j.ID] {
			continue
		}
		if j.Status != "queued" && j.Status != "paused" && j.Status != "publishing" {
			continue
		}
		blocked := false
		for _, other := range jobs {
			if other.ID == j.ID {
				continue
			}
			// Uncheckpointed interrupted/failed work is retained for intervention, not
			// silently displaced by a fresh writer after a daemon restart.
			if other.GitHubRepositoryID == j.GitHubRepositoryID && (e.issueActive[other.ID] || (other.Worktree != "" && (other.Status == "failed" || other.Status == "interrupted"))) {
				blocked = true
				break
			}
		}
		for _, revision := range e.reviewJobs {
			if revision.Issue.GitHubRepositoryID == j.GitHubRepositoryID && (e.issueActive[revision.IssueJobID] || (revision.Worktree != "" && revision.Status != "published")) {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		count := 0
		for _, a := range e.active {
			if a.projectID == j.ProjectID {
				count++
			}
		}
		if count >= j.Project.Policy.MaxParallel {
			continue
		}
		ctx, cancel := context.WithCancel(e.ctx)
		e.issueActive[j.ID] = true
		// Reserve capacity throughout workspace creation and publication too.
		e.active["issue/"+j.ID] = activeExecution{cancel: cancel, projectID: j.ProjectID}
		e.wg.Add(1)
		go e.workIssue(ctx, j)
	}
}
func (j savedIssueJob) workspace() issueWorkspace {
	return issueWorkspace{Path: j.Worktree, Branch: j.Branch, BaseSHA: j.BaseSHA, HeadSHA: j.HeadSHA}
}
func (j *savedIssueJob) setWorkspace(w issueWorkspace) {
	j.Worktree, j.Branch, j.BaseSHA, j.HeadSHA = w.Path, w.Branch, w.BaseSHA, w.HeadSHA
}
func (e *Engine) issueAdmission(ctx context.Context, j savedIssueJob) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	enabled := e.intakes[githubKey(j.ProjectID, j.RepositoryID)].Enabled
	cancelled := j.RunID != "" && e.runs[j.RunID].Run.Status == "cancelled"
	e.mu.Unlock()
	if !enabled {
		return fmt.Errorf("repository intake is disabled")
	}
	if cancelled {
		return fmt.Errorf("run was cancelled; candidate retained for intervention")
	}
	return nil
}
func (e *Engine) authorizeIssue(ctx context.Context, j *savedIssueJob) error {
	if err := e.issueAdmission(ctx, *j); err != nil {
		return err
	}
	connection, err := verifyIssueRepository(ctx, j.Repository.Path, j.Connection)
	if err != nil {
		return err
	}
	if j.BaseSHA != "" && connection.DefaultBranch != j.BaseBranch {
		return fmt.Errorf("default branch changed; candidate retained for intervention")
	}
	issue, err := fetchIntakeIssue(ctx, j.GitHubRepositoryID, j.Number)
	if err != nil {
		return err
	}
	if err = issueAuthorization(issue); err != nil {
		return err
	}
	if j.RunID != "" && (issue.Title != j.Title || issue.Body != j.Body) {
		return fmt.Errorf("issue changed since execution; candidate retained for intervention")
	}
	j.Connection = connection
	if j.RunID == "" {
		j.Title, j.Body, j.URL = issue.Title, issue.Body, issue.URL
		j.BaseBranch = connection.DefaultBranch
	}
	return e.issueAdmission(ctx, *j)
}
func (e *Engine) workIssue(ctx context.Context, j savedIssueJob) {
	defer e.wg.Done()
	defer func() {
		e.mu.Lock()
		a := e.active["issue/"+j.ID]
		if a.cancel != nil {
			a.cancel()
		}
		delete(e.active, "issue/"+j.ID)
		delete(e.issueActive, j.ID)
		e.mu.Unlock()
		e.notify()
	}()
	release, err := acquireIssueLease(j.Repository.Path, j.ID)
	if err != nil {
		j.Error = err.Error()
		_ = e.saveIssueJob(j)
		return
	}
	defer release()
	fail := func(status string, err error) {
		j.Status = status
		j.Error = err.Error()
		if ctx.Err() != nil {
			j.Status = "interrupted"
		}
		if e.saveIssueJob(j) != nil {
			return
		}
		runStatus := "failed"
		if status == "paused" {
			runStatus = "waiting"
		}
		if ctx.Err() != nil {
			runStatus = "interrupted"
		}
		_ = e.recordIssuePublication(j, runStatus, j.Error, nil)
	}
	if err = e.authorizeIssue(ctx, &j); err != nil {
		fail("paused", err)
		return
	}
	if j.RunID == "" {
		// Persist deterministic ownership intent before git creates either branch or
		// worktree. A crash here requires inspection, never automatic destructive retry.
		if j.BaseSHA == "" {
			j.Status = "working"
			j.Error = ""
			j.Worktree = filepath.Join(e.data, "worktrees", j.ID)
			j.Branch = "factory/issue-" + j.ID
			if err = e.saveIssueJob(j); err != nil {
				return
			}
			workspace, prepareErr := prepareIssueWorkspace(ctx, e.data, j.Repository.Path, j.ID, issueRemoteURL(j.Connection), j.BaseBranch)
			if prepareErr != nil {
				fail("failed", prepareErr)
				return
			}
			j.setWorkspace(workspace)
			if err = e.saveIssueJob(j); err != nil {
				return
			}
		}
		if err = e.authorizeIssue(ctx, &j); err != nil {
			fail("paused", err)
			return
		}
		if err = e.executeIssueAgent(ctx, &j); err != nil {
			fail("failed", err)
			return
		}
	}
	e.mu.Lock()
	runStatus := e.runs[j.RunID].Run.Nodes["implement"].Status
	e.mu.Unlock()
	if runStatus != "succeeded" {
		fail("failed", fmt.Errorf("implementation run ended %s; publication blocked", runStatus))
		return
	}
	if j.PRBody == "" {
		body, err := readIssuePRBody(j.Worktree, j.ID)
		if err != nil {
			fail("failed", err)
			return
		}
		j.PRBody = body
		if err = e.saveIssueJob(j); err != nil {
			return
		}
	}
	if err = removeIssuePRBody(j.Worktree, j.ID); err != nil {
		fail("failed", err)
		return
	}
	if j.HeadSHA == "" {
		if err = e.recordIssuePublication(j, "running", "Checkpointing validated issue candidate.", nil); err != nil {
			return
		}
		if err = e.authorizeIssue(ctx, &j); err != nil {
			fail("paused", err)
			return
		}
		workspace, err := checkpointIssueWorkspace(ctx, j.Repository.Path, j.workspace(), fmt.Sprintf("Resolve #%d: %s", j.Number, j.Title))
		if err != nil {
			fail("failed", err)
			return
		}
		j.setWorkspace(workspace)
		j.Status = "publishing"
		j.Error = ""
		if err = e.saveIssueJob(j); err != nil {
			return
		}
	}
	// An ambiguous prior POST is reconciled by immutable branch and ownership
	// marker before trying any new write. Closed PRs are never reopened/recreated.
	existing, err := findIssuePR(ctx, j)
	if err != nil {
		fail("publishing", err)
		return
	}
	if existing != nil {
		e.finishIssuePR(ctx, &j, *existing)
		return
	}
	if err = e.authorizeIssue(ctx, &j); err != nil {
		fail("paused", err)
		return
	}
	if err = e.recordIssuePublication(j, "running", "Rechecking authorization and publishing the draft candidate.", nil); err != nil {
		return
	}
	base, err := fetchIssueBaseSHA(ctx, j.Connection, j.BaseBranch)
	if err != nil {
		fail("publishing", err)
		return
	}
	if base != j.BaseSHA {
		fail("paused", fmt.Errorf("base branch advanced; candidate retained for revalidation, not published"))
		return
	}
	if err = e.authorizeIssue(ctx, &j); err != nil {
		fail("paused", err)
		return
	}
	if err = pushIssueWorkspace(ctx, j.Repository.Path, j.workspace(), issueRemoteURL(j.Connection)); err != nil {
		fail("publishing", err)
		return
	}
	if err = e.authorizeIssue(ctx, &j); err != nil {
		fail("paused", err)
		return
	}
	base, err = fetchIssueBaseSHA(ctx, j.Connection, j.BaseBranch)
	if err != nil {
		fail("publishing", err)
		return
	}
	if base != j.BaseSHA {
		fail("paused", fmt.Errorf("base branch advanced after push; draft creation blocked"))
		return
	}
	if err = e.authorizeIssue(ctx, &j); err != nil {
		fail("paused", err)
		return
	}
	pr, err := createIssuePR(ctx, j)
	if err != nil {
		fail("publishing", err)
		return
	}
	e.finishIssuePR(ctx, &j, pr)
}
func (e *Engine) finishIssuePR(ctx context.Context, j *savedIssueJob, pr issuePullRequest) {
	j.PullRequestNumber, j.PullRequestURL = pr.Number, pr.URL
	j.Status = "published"
	if pr.State == "closed" {
		j.Status = "closed"
	}
	j.Error = ""
	// Persist the external side effect before releasing its workspace. A restart
	// can finish cleanup even if the API no longer allows issue intake.
	if e.saveIssueJob(*j) != nil {
		return
	}
	if e.recordIssuePublication(*j, "succeeded", "Draft pull request recorded: "+pr.URL, &pr) != nil {
		return
	}
	if j.Worktree != "" {
		if err := cleanupIssueWorkspace(ctx, e.data, j.Repository.Path, j.ID, j.workspace()); err != nil {
			j.Error = "Worktree cleanup retained for safety: " + err.Error()
		} else {
			j.Worktree = ""
		}
		_ = e.saveIssueJob(*j)
	}
}
func (e *Engine) reconcileIssuePR(ctx context.Context, j savedIssueJob) error {
	if j.Status == "closed" && j.Worktree == "" {
		return nil
	}
	pr, err := findIssuePR(ctx, j)
	if err != nil {
		return err
	}
	if pr == nil {
		return nil
	}
	release, err := acquireIssueLease(j.Repository.Path, j.ID)
	if err != nil {
		return err
	}
	defer release()
	e.finishIssuePR(ctx, &j, *pr)
	return nil
}

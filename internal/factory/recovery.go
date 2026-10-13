package factory

import (
	"context"
	"errors"
	"fmt"
)

func recoveryStatus(action string) (string, error) {
	switch action {
	case "retry":
		return "released", nil
	case "dismiss":
		return "dismissed", nil
	default:
		return "", errors.New("recovery action must be retry or dismiss")
	}
}

func recoverableManagedStatus(status string) bool {
	return status == "failed" || status == "timed_out" || status == "interrupted"
}

func (e *Engine) finishManagedRecovery(issueJobID string, wake bool) {
	e.mu.Lock()
	delete(e.issueActive, issueJobID)
	e.mu.Unlock()
	if wake {
		select {
		case e.intakeWake <- struct{}{}:
		default:
		}
	}
}

func (e *Engine) recoverReviewJob(ctx context.Context, projectID, repositoryID, jobID, action, expectedBaseSHA, expectedHeadSHA string) (IntakeReport, error) {
	status, err := recoveryStatus(action)
	if err != nil {
		return IntakeReport{}, invalid(err)
	}
	e.mu.Lock()
	if err := e.healthyLocked(); err != nil {
		e.mu.Unlock()
		return IntakeReport{}, err
	}
	if _, err := e.repositoryLocked(projectID, repositoryID); err != nil {
		e.mu.Unlock()
		return IntakeReport{}, err
	}
	job, ok := e.reviewJobs[jobID]
	if !ok || job.ProjectID != projectID || job.RepositoryID != repositoryID {
		e.mu.Unlock()
		return IntakeReport{}, missing("revision job")
	}
	if !recoverableManagedStatus(job.Status) {
		e.mu.Unlock()
		return IntakeReport{}, conflict("revision job is not failed, timed out, or interrupted")
	}
	if job.BaseSHA != expectedBaseSHA || job.HeadSHA != expectedHeadSHA {
		e.mu.Unlock()
		return IntakeReport{}, conflict("revision job changed; refresh intake status before recovery")
	}
	if e.issueActive[job.IssueJobID] {
		e.mu.Unlock()
		return IntakeReport{}, conflict("pull request work is active")
	}
	e.issueActive[job.IssueJobID] = true
	e.mu.Unlock()
	wake := false
	defer func() { e.finishManagedRecovery(job.IssueJobID, wake) }()

	if job.Worktree != "" {
		release, _, err := acquireRepositoryLease(job.Issue.Repository.Path)
		if err != nil {
			return IntakeReport{}, conflict(err.Error())
		}
		if err := discardIssueWorkspace(ctx, e.data, job.Issue.Repository.Path, job.ID, job.workspace()); err != nil {
			release()
			return IntakeReport{}, conflict(fmt.Sprintf("discard retained revision workspace: %v", err))
		}
		release()
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	current := e.reviewJobs[job.ID]
	if current.Status != job.Status || current.Worktree != job.Worktree || current.BaseSHA != expectedBaseSHA || current.HeadSHA != expectedHeadSHA {
		return IntakeReport{}, conflict("revision job changed during recovery")
	}
	current.Status = status
	current.Error = ""
	current.Worktree = ""
	current.UpdatedAt = now()
	if err := e.store.save(record{"review-job", current.ID, current}); err != nil {
		e.fatalLocked(err)
		return IntakeReport{}, err
	}
	e.reviewJobs[current.ID] = current
	wake = action == "retry"
	return e.intakeReportLocked(projectID, repositoryID), nil
}

func (e *Engine) recoverSyncJob(ctx context.Context, projectID, repositoryID, jobID, action, expectedPullRequestHeadSHA, expectedBaseSHA, expectedHeadSHA string) (IntakeReport, error) {
	status, err := recoveryStatus(action)
	if err != nil {
		return IntakeReport{}, invalid(err)
	}
	e.mu.Lock()
	if err := e.healthyLocked(); err != nil {
		e.mu.Unlock()
		return IntakeReport{}, err
	}
	if _, err := e.repositoryLocked(projectID, repositoryID); err != nil {
		e.mu.Unlock()
		return IntakeReport{}, err
	}
	job, ok := e.syncJobs[jobID]
	if !ok || job.ProjectID != projectID || job.RepositoryID != repositoryID {
		e.mu.Unlock()
		return IntakeReport{}, missing("synchronization job")
	}
	if !recoverableManagedStatus(job.Status) {
		e.mu.Unlock()
		return IntakeReport{}, conflict("synchronization job is not failed, timed out, or interrupted")
	}
	if job.PullRequestHeadSHA != expectedPullRequestHeadSHA || job.BaseSHA != expectedBaseSHA || job.HeadSHA != expectedHeadSHA {
		e.mu.Unlock()
		return IntakeReport{}, conflict("synchronization job changed; refresh intake status before recovery")
	}
	if e.issueActive[job.IssueJobID] {
		e.mu.Unlock()
		return IntakeReport{}, conflict("pull request work is active")
	}
	e.issueActive[job.IssueJobID] = true
	e.mu.Unlock()
	wake := false
	defer func() { e.finishManagedRecovery(job.IssueJobID, wake) }()

	if job.Worktree != "" {
		release, _, err := acquireRepositoryLease(job.Issue.Repository.Path)
		if err != nil {
			return IntakeReport{}, conflict(err.Error())
		}
		if err := discardIssueWorkspace(ctx, e.data, job.Issue.Repository.Path, job.ID, job.workspace()); err != nil {
			release()
			return IntakeReport{}, conflict(fmt.Sprintf("discard retained synchronization workspace: %v", err))
		}
		release()
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	current := e.syncJobs[job.ID]
	if current.Status != job.Status || current.Worktree != job.Worktree || current.PullRequestHeadSHA != expectedPullRequestHeadSHA || current.BaseSHA != expectedBaseSHA || current.HeadSHA != expectedHeadSHA {
		return IntakeReport{}, conflict("synchronization job changed during recovery")
	}
	current.Status = status
	current.Error = ""
	current.Worktree = ""
	current.UpdatedAt = now()
	if err := e.store.save(record{"sync-job", current.ID, current}); err != nil {
		e.fatalLocked(err)
		return IntakeReport{}, err
	}
	e.syncJobs[current.ID] = current
	wake = action == "retry"
	return e.intakeReportLocked(projectID, repositoryID), nil
}

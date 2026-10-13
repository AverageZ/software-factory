package factory

import "fmt"

// PrepareRestart stops admission only when shutdown will not interrupt work.
// The daemon must close its server and engine after this method succeeds.
func (e *Engine) PrepareRestart() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.healthyLocked(); err != nil {
		return err
	}
	for _, job := range e.issueJobs {
		if job.Status == "working" || job.Status == "publishing" {
			return conflict(fmt.Sprintf("%s is active; cancel its run or wait for it to finish before rebuilding", managedWorkTitle("Issue", job.Number, job.Title)))
		}
	}
	for _, job := range e.reviewJobs {
		if job.Status == "queued" || job.Status == "working" || job.Status == "publishing" {
			return conflict(fmt.Sprintf("%s is active; cancel its run or wait for it to finish before rebuilding", managedPRTitle(job.PullRequestNumber, "revision", job.Issue.Title)))
		}
	}
	for _, job := range e.syncJobs {
		if job.Status == "queued" || job.Status == "working" || job.Status == "publishing" {
			return conflict(fmt.Sprintf("%s is active; cancel its run or wait for it to finish before rebuilding", managedPRTitle(job.PullRequestNumber, "synchronization", job.Issue.Title)))
		}
	}
	for _, saved := range e.runs {
		run := e.presentedRunLocked(saved)
		for _, node := range run.Nodes {
			if node.Status == "running" {
				return conflict(fmt.Sprintf("%s: task %q is active; cancel the run or wait for it to finish before rebuilding", run.Title, node.Name))
			}
		}
	}
	if len(e.active) != 0 || len(e.issueActive) != 0 {
		return conflict("Factory is finishing an active operation; wait for it to release its resources before rebuilding")
	}
	if len(e.intakeBusy) != 0 || len(e.githubBusy) != 0 {
		return conflict("a GitHub check is active; wait for it to finish before rebuilding")
	}
	e.stop()
	return nil
}

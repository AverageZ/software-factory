package factory

// PrepareRestart stops admission only when shutdown will not interrupt work.
// The daemon must close its server and engine after this method succeeds.
func (e *Engine) PrepareRestart() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.healthyLocked(); err != nil {
		return err
	}
	if len(e.active) != 0 || len(e.issueActive) != 0 {
		return conflict("work is active; wait for it to finish before rebuilding")
	}
	if len(e.intakeBusy) != 0 || len(e.githubBusy) != 0 {
		return conflict("a GitHub check is active; wait for it to finish before rebuilding")
	}
	for _, job := range e.issueJobs {
		if job.Status == "working" || job.Status == "publishing" {
			return conflict("an issue job is active; wait for it to finish before rebuilding")
		}
	}
	for _, job := range e.reviewJobs {
		if job.Status == "queued" || job.Status == "working" || job.Status == "publishing" {
			return conflict("a PR revision is active; wait for it to finish before rebuilding")
		}
	}
	for _, job := range e.syncJobs {
		if job.Status == "queued" || job.Status == "working" || job.Status == "publishing" {
			return conflict("a PR synchronization is active; wait for it to finish before rebuilding")
		}
	}
	for _, run := range e.runs {
		for _, node := range run.Run.Nodes {
			if node.Status == "running" {
				return conflict("a run is active; wait for it to finish before rebuilding")
			}
		}
	}
	e.stop()
	return nil
}

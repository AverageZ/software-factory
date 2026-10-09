package factory

import (
	"context"
	"fmt"
	"time"
)

func githubKey(projectID, repositoryID string) string {
	return projectID + "/" + repositoryID
}

func (e *Engine) repositoryLocked(projectID, repositoryID string) (Repository, error) {
	project, ok := e.projects[projectID]
	if !ok {
		return Repository{}, missing("project")
	}
	for _, repository := range project.Repositories {
		if repository.ID == repositoryID {
			return repository, nil
		}
	}
	return Repository{}, missing("repository")
}

func (e *Engine) repositoryGitHub(projectID, repositoryID string) (GitHubHealth, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.healthyLocked(); err != nil {
		return GitHubHealth{}, err
	}
	repository, err := e.repositoryLocked(projectID, repositoryID)
	if err != nil {
		return GitHubHealth{}, err
	}
	health, ok := e.github[githubKey(projectID, repositoryID)]
	if !ok || health.RepositoryPath != repository.Path {
		return emptyGitHubHealth(repository.Path), nil
	}
	return copyJSON(health), nil
}

// A repository cannot be moved or removed while an explicit GitHub action is
// running. Project metadata and other repositories remain independently editable.
func (e *Engine) checkGitHubProjectUpdateLocked(project Project) error {
	previous, exists := e.projects[project.ID]
	if !exists {
		return nil
	}
	for _, repository := range previous.Repositories {
		if !e.githubBusy[githubKey(project.ID, repository.ID)] {
			continue
		}
		unchanged := false
		for _, next := range project.Repositories {
			if next.ID == repository.ID && next.Path == repository.Path {
				unchanged = true
				break
			}
		}
		if !unchanged {
			return conflict("GitHub setup is in progress for repository " + repository.Name + "; wait before removing it or changing its path")
		}
	}
	return nil
}

// Network/subprocess work never holds the workflow engine lock. A same-repository
// operation is exclusive and shutdown waits until its final report is persisted.
func (e *Engine) updateRepositoryGitHub(ctx context.Context, projectID, repositoryID string, target *GitHubTarget) (GitHubHealth, error) {
	if target != nil && (target.Host != "github.com" || target.RepositoryID <= 0) {
		return GitHubHealth{}, invalid(fmt.Errorf("label setup requires the checked github.com host and positive repositoryId"))
	}
	key := githubKey(projectID, repositoryID)
	e.mu.Lock()
	if err := e.healthyLocked(); err != nil {
		e.mu.Unlock()
		return GitHubHealth{}, err
	}
	repository, err := e.repositoryLocked(projectID, repositoryID)
	if err != nil {
		e.mu.Unlock()
		return GitHubHealth{}, err
	}
	if e.githubBusy[key] {
		e.mu.Unlock()
		return GitHubHealth{}, conflict("a GitHub check or label setup is already in progress for this repository")
	}
	e.githubBusy[key] = true
	e.wg.Add(1)
	e.mu.Unlock()
	defer e.wg.Done()
	defer func() {
		e.mu.Lock()
		delete(e.githubBusy, key)
		e.mu.Unlock()
	}()

	operation, cancel := context.WithTimeout(ctx, 2*time.Minute)
	stop := context.AfterFunc(e.ctx, cancel)
	defer stop()
	defer cancel()
	var health GitHubHealth
	if target == nil {
		health = checkGitHub(operation, repository.Path)
	} else {
		health = createGitHubLabels(operation, repository.Path, *target)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fatal != nil {
		return GitHubHealth{}, fmt.Errorf("persistence failed: %w", e.fatal)
	}
	if err := e.store.save(record{"github", key, health}); err != nil {
		e.fatalLocked(err)
		return GitHubHealth{}, err
	}
	e.github[key] = health
	return copyJSON(health), nil
}

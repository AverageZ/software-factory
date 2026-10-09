package factory

import (
	"context"
	"testing"
)

func TestGitHubHealthCannotFollowRepositoryPathChange(t *testing.T) {
	e := &Engine{
		ctx: context.Background(),
		projects: map[string]Project{"project": {
			ID: "project", Repositories: []Repository{{ID: "repo", Path: "/new-checkout"}},
		}},
		github: map[string]GitHubHealth{githubKey("project", "repo"): {
			RepositoryPath: "/previous-checkout", Status: "ready", CheckedAt: "2026-10-08T00:00:00Z",
			Connection: &GitHubConnection{Host: "github.com", ID: 123, CanManageLabels: true},
		}},
	}
	health, err := e.repositoryGitHub("project", "repo")
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != "unchecked" || health.Connection != nil || health.CheckedAt != "" || health.RepositoryPath != "/new-checkout" {
		t.Fatalf("old connection/permission was exposed for a different checkout: %+v", health)
	}
	if _, err := e.repositoryGitHub("project", "removed-repo"); err == nil {
		t.Fatal("unknown repository returned usable health")
	}
}

func TestGitHubActionPreventsRetargetingButNotUnrelatedProjectEdits(t *testing.T) {
	original := Project{ID: "project", Repositories: []Repository{
		{ID: "busy", Name: "Busy", Path: "/original"},
		{ID: "other", Path: "/other"},
	}}
	e := &Engine{projects: map[string]Project{"project": original}, githubBusy: map[string]bool{githubKey("project", "busy"): true}}
	for _, repositories := range [][]Repository{
		{{ID: "busy", Path: "/different-target"}},
		{{ID: "other", Path: "/other"}},
	} {
		if err := e.checkGitHubProjectUpdateLocked(Project{ID: "project", Repositories: repositories}); err == nil {
			t.Fatal("active GitHub operation allowed its repository to be moved or removed")
		}
	}
	allowed := Project{ID: "project", Name: "Renamed project", Repositories: []Repository{
		{ID: "busy", Name: "Renamed repository", Path: "/original"},
		{ID: "other", Path: "/changed-other"},
	}}
	if err := e.checkGitHubProjectUpdateLocked(allowed); err != nil {
		t.Fatalf("unrelated project edit blocked: %v", err)
	}
}

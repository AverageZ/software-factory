package factory

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestGitHubOriginParsingAndCredentialSafety(t *testing.T) {
	for _, input := range []string{
		"git@github.com:AverageZ/strategy-game.git",
		"ssh://git@github.com/AverageZ/strategy-game.git",
		"ssh://git@github.com:22/AverageZ/strategy-game.git",
		"https://github.com/AverageZ/strategy-game.git",
		"https://GITHUB.COM:443/AverageZ/strategy-game/",
		"https://secret-token@github.com/AverageZ/strategy-game.git",
		"https://user:secret-token@github.com/AverageZ/strategy-game.git",
		"https://user:secret%2Dtoken@github.com/AverageZ/strategy-game.git",
	} {
		remote, err := parseGitHubRemote(input)
		if err != nil {
			t.Errorf("supported origin rejected: %v", err)
			continue
		}
		want := gitHubRemote{Host: "github.com", Owner: "AverageZ", Name: "strategy-game"}
		if remote != want {
			t.Errorf("parsed destination = %#v, want %#v", remote, want)
		}
		if strings.Contains(fmt.Sprintf("%#v", remote), "secret") {
			t.Fatal("parsed origin retained URL credentials")
		}
	}
	remote, err := parseGitHubRemote("https://github.com/owner/repository.git.git")
	if err != nil || remote.Name != "repository.git" {
		t.Fatalf("a repository name ending in .git was changed: remote=%#v, error=%v", remote, err)
	}

	for _, input := range []string{
		"",
		"/local/repository",
		"http://github.com/owner/repo",
		"git://github.com/owner/repo",
		"https://secret-token@github.com.evil.example/owner/repo",
		"https://secret-token@github.example/owner/repo",
		"https://secret-token@github.com:8443/owner/repo",
		"ssh://secret-token@github.com/owner/repo",
		"ssh://git:secret-token@github.com/owner/repo",
		"git@github.com:owner/repo/extra",
		"git@github.com:/owner/repo",
		"https://github.com/owner",
		"https://github.com//repo",
		"https://github.com/owner/..",
		"https://github.com/../repo",
		"https://github.com/owner/.git",
		"https://github.com/owner/repo/extra",
		"https://github.com/owner%2frepo/other",
		"https://github.com/owner/%2e%2e",
		"https://secret-token@github.com/owner/%invalid",
		"https://github.com/owner/repo?token=secret-token",
		"https://github.com/owner/repo?",
		"https://github.com/owner/repo#secret-token",
		"https://github.com/owner/repo#",
		"git@github.com:owner/repo?secret-token",
		"git@github.com:owner/repo\ngit@github.com:other/repo",
		"https://github.com/owner/repo\x00",
		" https://github.com/owner/repo",
	} {
		remote, err := parseGitHubRemote(input)
		if err == nil {
			t.Errorf("unsafe or ambiguous origin accepted: destination=%#v", remote)
			continue
		}
		if remote != (gitHubRemote{}) {
			t.Errorf("rejected origin retained a partial destination: %#v", remote)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("origin error disclosed URL credentials")
		}
	}
}

func TestGitHubLabelsReconcilePagesWithoutReplacingExistingMetadata(t *testing.T) {
	pages := [][]gitHubLabelResponse{
		{
			{Name: "AGENT:RUN", Color: "fedcba", Description: "Existing repository authorization policy"},
			{Name: "unrelated", Color: "123456", Description: "A human-owned label"},
			{Name: "agent/status:active-extra", Color: "654321", Description: "Not the exact Factory label"},
		},
		{
			{Name: "Agent/Status:Complete", Color: "abcdef", Description: ""},
			{Name: "agent/phase:reviewing", Color: "112233", Description: "Existing review instructions"},
		},
	}
	original := [][]gitHubLabelResponse{
		append([]gitHubLabelResponse(nil), pages[0]...),
		append([]gitHubLabelResponse(nil), pages[1]...),
	}
	labels := reconcileGitHubLabels(pages)
	observed := make(map[string]GitHubLabel, len(labels))
	for _, label := range labels {
		observed[label.Name] = label
	}
	for _, existing := range []struct {
		name        string
		color       string
		description string
	}{
		{"agent:run", "fedcba", "Existing repository authorization policy"},
		{"agent/status:complete", "abcdef", ""},
		{"agent/phase:reviewing", "112233", "Existing review instructions"},
	} {
		label := observed[existing.name]
		if !label.Present || label.Color != existing.color || label.Description != existing.description {
			t.Errorf("existing label metadata or case-insensitive presence lost for %s: %#v", existing.name, label)
		}
	}
	if observed["agent/status:active"].Present {
		t.Fatal("a similar name was mistaken for an existing Factory label")
	}
	if _, found := observed["unrelated"]; found {
		t.Fatal("an unrelated repository label entered the Factory setup catalog")
	}
	if !reflect.DeepEqual(pages, original) {
		t.Fatal("reconciliation mutated the repository's observed labels")
	}

	refreshed := reconcileGitHubLabels([][]gitHubLabelResponse{{{Name: "agent/status:active", Color: "998877", Description: "Created elsewhere"}}})
	for _, label := range refreshed {
		if label.Name == "agent/status:active" && !label.Present {
			t.Fatal("a newly observed label remained missing after refresh")
		}
		if label.Name == "agent:run" && label.Present {
			t.Fatal("a label deleted elsewhere remained present after refresh")
		}
	}
}

package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type GitHubConnection struct {
	Host            string `json:"host"`
	ID              int64  `json:"id"`
	Owner           string `json:"owner"`
	Name            string `json:"name"`
	URL             string `json:"url"`
	DefaultBranch   string `json:"defaultBranch"`
	Account         string `json:"account"`
	CanPush         bool   `json:"canPush"`
	CanManageLabels bool   `json:"canManageLabels"`
	Archived        bool   `json:"archived"`
	IssuesEnabled   bool   `json:"issuesEnabled"`
}

type GitHubCheck struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type GitHubLabel struct {
	Name        string `json:"name"`
	Group       string `json:"group"`
	Color       string `json:"color"`
	Description string `json:"description"`
	Present     bool   `json:"present"`
}

type GitHubHealth struct {
	RepositoryPath string            `json:"repositoryPath"`
	CheckedAt      string            `json:"checkedAt"`
	Status         string            `json:"status"`
	Connection     *GitHubConnection `json:"connection,omitempty"`
	Checks         []GitHubCheck     `json:"checks"`
	Labels         []GitHubLabel     `json:"labels"`
	CreatedLabels  []string          `json:"createdLabels"`
	SetupError     string            `json:"setupError,omitempty"`
}

type GitHubTarget struct {
	Host         string `json:"host"`
	RepositoryID int64  `json:"repositoryId"`
}

func gitHubLabelCatalog() []GitHubLabel {
	return []GitHubLabel{
		{Name: "agent:run", Group: "authorization", Color: "0e8a16", Description: "Authorize Factory activity for this issue or pull request."},
		{Name: "agent/workflow:bug", Group: "workflow", Color: "1d76db", Description: "Select the Factory bug investigation and repair workflow."},
		{Name: "agent/workflow:review", Group: "workflow", Color: "1d76db", Description: "Select the Factory pull request review workflow."},
		{Name: "agent/workflow:rollback", Group: "workflow", Color: "1d76db", Description: "Select the Factory rollback workflow."},
		{Name: "agent/mode:observe", Group: "mode", Color: "5319e7", Description: "Observe and report without changing code."},
		{Name: "agent/mode:repair", Group: "mode", Color: "5319e7", Description: "Allow Factory to propose corrective code changes."},
		{Name: "agent/mode:manage", Group: "mode", Color: "5319e7", Description: "Allow Factory management actions subject to approval gates."},
		{Name: "agent/status:queued", Group: "status", Color: "fbca04", Description: "Factory work is waiting to start."},
		{Name: "agent/status:active", Group: "status", Color: "fbca04", Description: "Factory work is in progress."},
		{Name: "agent/status:paused", Group: "status", Color: "fbca04", Description: "Factory work is paused."},
		{Name: "agent/status:awaiting-human", Group: "status", Color: "fbca04", Description: "Factory work is waiting for human input or approval."},
		{Name: "agent/status:blocked", Group: "status", Color: "fbca04", Description: "Factory work cannot proceed until a blocker is resolved."},
		{Name: "agent/status:failed", Group: "status", Color: "fbca04", Description: "Factory work ended unsuccessfully."},
		{Name: "agent/status:complete", Group: "status", Color: "fbca04", Description: "Factory work is complete."},
		{Name: "agent/phase:triaging", Group: "phase", Color: "c5def5", Description: "Factory is classifying and investigating the subject."},
		{Name: "agent/phase:implementing", Group: "phase", Color: "c5def5", Description: "Factory is implementing a proposed change."},
		{Name: "agent/phase:validating", Group: "phase", Color: "c5def5", Description: "Factory is checking the proposed change."},
		{Name: "agent/phase:reviewing", Group: "phase", Color: "c5def5", Description: "Factory is reviewing the proposed change."},
		{Name: "agent/phase:publishing", Group: "phase", Color: "c5def5", Description: "Factory is publishing an approved result."},
		{Name: "agent/phase:monitoring", Group: "phase", Color: "c5def5", Description: "Factory is monitoring a published result."},
		{Name: "agent/phase:recovering", Group: "phase", Color: "c5def5", Description: "Factory is recovering from a failed operation."},
	}
}

func emptyGitHubHealth(path string) GitHubHealth {
	return GitHubHealth{
		RepositoryPath: path,
		Status:         "unchecked",
		Checks:         []GitHubCheck{},
		Labels:         gitHubLabelCatalog(),
		CreatedLabels:  []string{},
	}
}

type gitHubRemote struct {
	Host  string
	Owner string
	Name  string
}

// Never retain or echo the raw remote: HTTPS user information can contain a token.
func parseGitHubRemote(raw string) (gitHubRemote, error) {
	invalid := func(message string) (gitHubRemote, error) {
		return gitHubRemote{}, errors.New(message)
	}
	if raw == "" || strings.IndexFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return invalid("Origin must be a single GitHub HTTPS or SSH URL without whitespace.")
	}
	var host, path string
	if strings.HasPrefix(raw, "git@") && !strings.Contains(raw, "://") {
		var ok bool
		host, path, ok = strings.Cut(strings.TrimPrefix(raw, "git@"), ":")
		if !ok {
			return invalid("Origin is not a supported GitHub SSH URL.")
		}
	} else {
		u, err := url.Parse(raw)
		if err != nil {
			return invalid("Origin is not a valid GitHub HTTPS or SSH URL.")
		}
		if u.Scheme != "https" && u.Scheme != "ssh" {
			return invalid("Origin must use HTTPS or SSH; other URL schemes are not supported.")
		}
		if u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") || u.RawPath != "" {
			return invalid("Origin must not contain a query, fragment, or escaped repository path.")
		}
		if u.Scheme == "ssh" {
			if u.User == nil || u.User.Username() != "git" {
				return invalid("GitHub SSH origins must use the git user.")
			}
			if _, password := u.User.Password(); password {
				return invalid("GitHub SSH origins must not contain a password.")
			}
		}
		if port := u.Port(); port != "" && !((u.Scheme == "https" && port == "443") || (u.Scheme == "ssh" && port == "22")) {
			return invalid("Origin uses an unsupported GitHub port.")
		}
		host, path = u.Hostname(), strings.TrimPrefix(u.Path, "/")
	}
	if !strings.EqualFold(host, "github.com") {
		return invalid("Only github.com origins are supported; configure an unambiguous GitHub origin.")
	}
	path = strings.TrimSuffix(path, "/")
	owner, name, ok := strings.Cut(path, "/")
	name = strings.TrimSuffix(name, ".git")
	if !ok || !validGitHubPathPart(owner, false) || !validGitHubPathPart(name, true) {
		return invalid("Origin must identify exactly one GitHub owner and repository.")
	}
	return gitHubRemote{Host: "github.com", Owner: owner, Name: name}, nil
}

func validGitHubPathPart(value string, repository bool) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, c := range value {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || repository && (c == '_' || c == '.') {
			continue
		}
		return false
	}
	return true
}

// Error text is intentionally constructed, not copied from subprocess output.
// Both git and gh may echo credential-bearing remote URLs or HTTP headers.
type gitHubCommandError struct {
	message    string
	httpStatus int
}

func (e *gitHubCommandError) Error() string { return e.message }

func runGitHubCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	commandContext, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandContext, binary, args...)
	cmd.WaitDelay = time.Second
	environment := os.Environ()
	if binary == "git" {
		// A server launched from a Git hook must still inspect the configured path.
		filtered := environment[:0]
		for _, setting := range environment {
			key, _, _ := strings.Cut(setting, "=")
			switch key {
			case "GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_CONFIG", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS":
				continue
			}
			filtered = append(filtered, setting)
		}
		environment = filtered
	}
	cmd.Env = append(environment, "GH_PROMPT_DISABLED=1", "GH_DEBUG=", "GIT_TERMINAL_PROMPT=0")
	data, err := cmd.Output()
	if err == nil {
		return data, nil
	}
	failure := &gitHubCommandError{}
	switch {
	case errors.Is(commandContext.Err(), context.DeadlineExceeded):
		failure.message = binary + " command timed out; check connectivity and retry."
	case errors.Is(commandContext.Err(), context.Canceled):
		failure.message = binary + " command was canceled; refresh to observe the current state."
	case errors.Is(err, exec.ErrNotFound):
		failure.message = binary + " is not installed or is not on the Factory server's PATH."
	default:
		failure.message = binary + " command failed."
		var exit *exec.ExitError
		if binary != "gh" || !errors.As(err, &exit) {
			break
		}
		stderr := string(exit.Stderr)
		if start := strings.LastIndex(stderr, "(HTTP "); start >= 0 && len(stderr) >= start+10 && stderr[start+9] == ')' {
			failure.httpStatus, _ = strconv.Atoi(stderr[start+6 : start+9])
		}
		switch failure.httpStatus {
		case 401:
			failure.message = "GitHub authentication failed (HTTP 401); authenticate gh for github.com on the Factory server."
		case 403:
			failure.message = "GitHub denied access (HTTP 403); check repository permissions, token scopes, and rate limits."
		case 404:
			failure.message = "GitHub resource was not found or is not accessible to this account (HTTP 404)."
		case 422:
			failure.message = "GitHub rejected the label request (HTTP 422)."
		case 429:
			failure.message = "GitHub rate limit reached (HTTP 429); retry after the limit resets."
		default:
			if failure.httpStatus != 0 {
				failure.message = fmt.Sprintf("GitHub request failed (HTTP %d).", failure.httpStatus)
			} else if exit.ExitCode() == 4 || strings.Contains(stderr, "gh auth login") || strings.Contains(stderr, "not logged in") {
				failure.message = "gh is not authenticated for github.com; authenticate it on the Factory server."
			} else {
				lower := strings.ToLower(stderr)
				for _, marker := range []string{"error connecting", "dial tcp", "no such host", "connection refused", "tls", "i/o timeout", "network is unreachable"} {
					if strings.Contains(lower, marker) {
						failure.message = "Cannot reach GitHub; check the Factory server's network and TLS configuration."
						break
					}
				}
			}
		}
	}
	return nil, failure
}

func gitHubAPI(ctx context.Context, method, endpoint string, options ...string) ([]byte, error) {
	args := []string{"api", "--hostname", "github.com", "--method", method, endpoint}
	args = append(args, options...)
	return runGitHubCommand(ctx, "gh", args...)
}

func discoverGitHubOrigin(ctx context.Context, repositoryPath string) (gitHubRemote, error) {
	absolute, err := filepath.Abs(repositoryPath)
	if err != nil || repositoryPath == "" {
		return gitHubRemote{}, errors.New("Configure an existing Git repository root path.")
	}
	root, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return gitHubRemote{}, errors.New("The configured repository path cannot be resolved.")
	}
	data, err := runGitHubCommand(ctx, "git", "-C", root, "rev-parse", "--show-toplevel")
	if err != nil {
		return gitHubRemote{}, fmt.Errorf("Cannot verify the configured Git repository root: %w", err)
	}
	actual, err := filepath.EvalSymlinks(strings.TrimSuffix(string(data), "\n"))
	if err != nil || actual != root {
		return gitHubRemote{}, errors.New("The configured path must be the Git working tree root, not a parent or subdirectory.")
	}
	data, err = runGitHubCommand(ctx, "git", "-C", root, "remote", "get-url", "--all", "origin")
	if err != nil {
		return gitHubRemote{}, fmt.Errorf("Cannot read origin; configure exactly one GitHub origin URL: %w", err)
	}
	origins := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(origins) != 1 {
		return gitHubRemote{}, errors.New("Origin has multiple URLs; configure exactly one unambiguous GitHub origin.")
	}
	return parseGitHubRemote(origins[0])
}

type gitHubRepositoryResponse struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
	Archived    *bool `json:"archived"`
	Disabled    *bool `json:"disabled"`
	HasIssues   *bool `json:"has_issues"`
	Permissions *struct {
		Admin    bool `json:"admin"`
		Maintain bool `json:"maintain"`
		Push     bool `json:"push"`
	} `json:"permissions"`
}

type gitHubLabelResponse struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

func reconcileGitHubLabels(pages [][]gitHubLabelResponse) []GitHubLabel {
	labels := gitHubLabelCatalog()
	indices := make(map[string]int, len(labels))
	for i, label := range labels {
		indices[label.Name] = i
	}
	for _, page := range pages {
		for _, observed := range page {
			if i, found := indices[strings.ToLower(observed.Name)]; found {
				labels[i].Present = true
				labels[i].Color = observed.Color
				labels[i].Description = observed.Description
			}
		}
	}
	return labels
}

func readGitHubLabels(ctx context.Context, repositoryID int64) ([]GitHubLabel, error) {
	endpoint := fmt.Sprintf("repositories/%d/labels?per_page=100", repositoryID)
	data, err := gitHubAPI(ctx, "GET", endpoint, "--paginate", "--slurp")
	if err != nil {
		return nil, err
	}
	var pages [][]gitHubLabelResponse
	if err := json.Unmarshal(data, &pages); err != nil || len(pages) == 0 {
		return nil, errors.New("GitHub returned an unreadable paginated label list.")
	}
	for _, page := range pages {
		if page == nil {
			return nil, errors.New("GitHub returned an incomplete paginated label list.")
		}
		for _, label := range page {
			if label.Name == "" {
				return nil, errors.New("GitHub returned a label without a name.")
			}
		}
	}
	return reconcileGitHubLabels(pages), nil
}

func setGitHubCheck(health *GitHubHealth, id, status, message string) {
	check := GitHubCheck{ID: id, Status: status, Message: message}
	found := false
	for i := range health.Checks {
		if health.Checks[i].ID == id {
			health.Checks[i] = check
			found = true
			break
		}
	}
	if !found {
		health.Checks = append(health.Checks, check)
	}
	health.Status = "ready"
	for _, existing := range health.Checks {
		if existing.Status == "error" {
			health.Status = "blocked"
			break
		}
		if existing.Status == "warning" {
			health.Status = "attention"
		}
	}
}

func setGitHubLabelCheck(health *GitHubHealth) {
	missing := 0
	for _, label := range health.Labels {
		if !label.Present {
			missing++
		}
	}
	if missing > 0 {
		setGitHubCheck(health, "labels", "warning", fmt.Sprintf("%d of %d Factory label definitions are missing; creating them requires an explicit action.", missing, len(health.Labels)))
	} else {
		setGitHubCheck(health, "labels", "ok", "All Factory label definitions are present. This is GitHub setup readiness only, not workflow or branch-protection validation.")
	}
}

func checkGitHub(ctx context.Context, repositoryPath string) (health GitHubHealth) {
	health = emptyGitHubHealth(repositoryPath)
	defer func() { health.CheckedAt = time.Now().UTC().Format(time.RFC3339) }()
	remote, err := discoverGitHubOrigin(ctx, repositoryPath)
	if err != nil {
		setGitHubCheck(&health, "origin", "error", err.Error())
		return health
	}
	setGitHubCheck(&health, "origin", "ok", "Verified the configured Git working tree root and its single github.com origin.")
	data, err := gitHubAPI(ctx, "GET", "user")
	if err != nil {
		setGitHubCheck(&health, "authentication", "error", err.Error())
		return health
	}
	var account struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(data, &account); err != nil || account.Login == "" {
		setGitHubCheck(&health, "authentication", "error", "GitHub did not return an authenticated account.")
		return health
	}
	setGitHubCheck(&health, "authentication", "ok", "Authenticated gh account: "+account.Login+".")
	data, err = gitHubAPI(ctx, "GET", "repos/"+remote.Owner+"/"+remote.Name)
	if err != nil {
		setGitHubCheck(&health, "repository", "error", err.Error())
		return health
	}
	var repository gitHubRepositoryResponse
	if err := json.Unmarshal(data, &repository); err != nil || repository.ID <= 0 || !validGitHubPathPart(repository.Owner.Login, false) || !validGitHubPathPart(repository.Name, true) || repository.Archived == nil || repository.Disabled == nil || repository.HasIssues == nil {
		setGitHubCheck(&health, "repository", "error", "GitHub returned incomplete repository identity or availability information.")
		return health
	}
	connection := &GitHubConnection{
		Host:          remote.Host,
		ID:            repository.ID,
		Owner:         repository.Owner.Login,
		Name:          repository.Name,
		URL:           "https://github.com/" + repository.Owner.Login + "/" + repository.Name,
		DefaultBranch: repository.DefaultBranch,
		Account:       account.Login,
		Archived:      *repository.Archived,
		IssuesEnabled: *repository.HasIssues,
	}
	health.Connection = connection
	setGitHubCheck(&health, "repository", "ok", fmt.Sprintf("GitHub verified %s/%s (repository ID %d).", connection.Owner, connection.Name, connection.ID))
	if *repository.Disabled {
		setGitHubCheck(&health, "availability", "error", "GitHub has disabled this repository; label setup is blocked.")
	} else if connection.Archived {
		setGitHubCheck(&health, "availability", "error", "This repository is archived and read-only; label setup is blocked.")
	} else {
		setGitHubCheck(&health, "availability", "ok", "The repository is neither disabled nor archived.")
	}
	if repository.Permissions == nil {
		setGitHubCheck(&health, "permissions", "error", "GitHub did not report repository permissions; label management cannot be verified.")
	} else {
		connection.CanPush = repository.Permissions.Push
		connection.CanManageLabels = repository.Permissions.Push || repository.Permissions.Admin || repository.Permissions.Maintain
		if connection.CanManageLabels {
			setGitHubCheck(&health, "permissions", "ok", "Repository permissions report label-management access (push, maintain, or admin). Token scopes and branch protections are not verified.")
		} else {
			setGitHubCheck(&health, "permissions", "error", "The account lacks reported push, maintain, or admin repository permission required for label setup. Token scopes and branch protections are not verified.")
		}
	}
	if connection.IssuesEnabled {
		setGitHubCheck(&health, "issues", "ok", "GitHub Issues are enabled; no issue intake or other workflow is started.")
	} else {
		setGitHubCheck(&health, "issues", "warning", "GitHub Issues are disabled; label definitions may still be used on pull requests.")
	}
	if connection.DefaultBranch == "" {
		setGitHubCheck(&health, "default-branch", "warning", "GitHub did not report a default branch.")
	}
	labels, err := readGitHubLabels(ctx, connection.ID)
	if err != nil {
		setGitHubCheck(&health, "labels", "error", "Label presence could not be verified: "+err.Error())
		return health
	}
	health.Labels = labels
	setGitHubLabelCheck(&health)
	return health
}

func failGitHubSetup(health *GitHubHealth, message string) {
	health.SetupError = message
	setGitHubCheck(health, "setup", "error", message)
}

func createGitHubLabels(ctx context.Context, repositoryPath string, expected GitHubTarget) (health GitHubHealth) {
	health = checkGitHub(ctx, repositoryPath)
	defer func() { health.CheckedAt = time.Now().UTC().Format(time.RFC3339) }()
	if health.Connection == nil {
		failGitHubSetup(&health, "Label setup is blocked because the GitHub repository identity could not be verified; no labels were written.")
		return health
	}
	if expected.Host != "github.com" || expected.RepositoryID <= 0 || expected.Host != health.Connection.Host || expected.RepositoryID != health.Connection.ID {
		failGitHubSetup(&health, "The verified GitHub host or repository ID does not match the acknowledged destination; no labels were written. Check the connection and confirm the current destination.")
		return health
	}
	if health.Status == "blocked" || !health.Connection.CanManageLabels || health.Connection.Archived {
		failGitHubSetup(&health, "Label setup is blocked by the connection checks; no labels were written. Resolve the reported errors before retrying.")
		return health
	}
	// Address the verified immutable repository ID, never a potentially reassigned name.
	endpoint := fmt.Sprintf("repositories/%d/labels", health.Connection.ID)
	attempted := false
	for i := range health.Labels {
		label := health.Labels[i]
		if label.Present {
			continue
		}
		attempted = true
		data, err := gitHubAPI(ctx, "POST", endpoint, "--raw-field", "name="+label.Name, "--raw-field", "color="+label.Color, "--raw-field", "description="+label.Description)
		if err == nil {
			var created gitHubLabelResponse
			if decodeErr := json.Unmarshal(data, &created); decodeErr != nil || !strings.EqualFold(created.Name, label.Name) {
				failGitHubSetup(&health, "GitHub accepted a label request but its response could not be verified. Refresh the connection before retrying.")
				break
			}
			health.CreatedLabels = append(health.CreatedLabels, created.Name)
			health.Labels[i].Present = true
			health.Labels[i].Color = created.Color
			health.Labels[i].Description = created.Description
			continue
		}
		var commandError *gitHubCommandError
		if errors.As(err, &commandError) && commandError.httpStatus == 422 {
			labels, refreshErr := readGitHubLabels(ctx, health.Connection.ID)
			if refreshErr == nil {
				health.Labels = labels
				if health.Labels[i].Present {
					// Another actor created this name; do not alter their metadata.
					continue
				}
			}
		}
		failGitHubSetup(&health, "Could not create "+label.Name+": "+err.Error()+" Previously created labels were retained; an explicit retry creates only names still missing.")
		break
	}
	if attempted {
		labels, err := readGitHubLabels(ctx, health.Connection.ID)
		if err != nil {
			message := "Could not refresh labels after setup: " + err.Error() + " Label presence below reflects only the last confirmed observations; refresh before retrying."
			setGitHubCheck(&health, "labels", "error", message)
			if health.SetupError != "" {
				message = health.SetupError + " " + message
			}
			failGitHubSetup(&health, message)
			return health
		}
		health.Labels = labels
		setGitHubLabelCheck(&health)
	}
	if health.SetupError == "" {
		for _, label := range health.Labels {
			if !label.Present {
				failGitHubSetup(&health, "Labels are still missing after setup; the repository may have changed concurrently. Existing labels were retained. Check the connection and explicitly retry.")
				return health
			}
		}
		setGitHubCheck(&health, "setup", "ok", fmt.Sprintf("Created %d missing label definitions. Existing labels and their metadata were left unchanged.", len(health.CreatedLabels)))
	}
	return health
}

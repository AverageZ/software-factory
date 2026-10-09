package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type issueWorkspace struct {
	Path    string
	Branch  string
	BaseSHA string
	HeadSHA string
}

// Ownership lives with the repository, not the daemon data directory. In
// particular, an abandoned, uncheckpointed worktree must block other daemons.
// Records and branches survive cleanup as durable evidence of ownership.
type issueWorkspaceOwner struct {
	issueWorkspace
	CommonDir string
	GitDir    string
}

type issueWorktreeEntry struct {
	Path   string
	Branch string
	Head   string
	Locked bool
}

func issueGit(ctx context.Context, path string, args ...string) ([]byte, error) {
	commandContext, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// Git publication uses the same authenticated account as the GitHub API,
	// without requiring or modifying the user's global credential-helper setup.
	options := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "commit.gpgSign=false", "-c", "http.followRedirects=false", "-c", "credential.helper=", "-c", "credential.helper=!gh auth git-credential", "-C", path}
	cmd := exec.CommandContext(commandContext, "git", append(options, args...)...)
	cmd.WaitDelay = time.Second
	for _, setting := range os.Environ() {
		key, _, _ := strings.Cut(setting, "=")
		if !strings.HasPrefix(key, "GIT_") {
			cmd.Env = append(cmd.Env, setting)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1")
	data, err := cmd.Output()
	if commandContext.Err() != nil {
		return nil, commandContext.Err()
	}
	if err != nil {
		// Do not expose subprocess output: a remote or credential helper can
		// echo credentials. Keep the exit error available for status checks.
		return nil, fmt.Errorf("workspace git %s failed: %w", args[0], err)
	}
	return data, nil
}

func issueGitText(ctx context.Context, path string, args ...string) (string, error) {
	data, err := issueGit(ctx, path, args...)
	return strings.TrimSpace(string(data)), err
}

func issueGitExit(err error, code int) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == code
}

func validIssueJobID(jobID string) bool {
	if len(jobID) == 0 || len(jobID) > 128 {
		return false
	}
	for _, c := range jobID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func issueCanonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func issueRepositoryCommonDir(ctx context.Context, repositoryPath string) (string, error) {
	root, err := issueCanonicalPath(repositoryPath)
	if err != nil {
		return "", fmt.Errorf("resolve repository: %w", err)
	}
	top, err := issueGitText(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	top, err = issueCanonicalPath(top)
	if err != nil || top != root {
		return "", errors.New("repository path must identify an exact Git checkout root")
	}
	common, err := issueGitText(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return issueCanonicalPath(common)
}

// Only the configured data root may resolve through a symlink (for example
// macOS /var). Owned children and administrative directories may not.
func issueSafeDirectory(path string, create bool) error {
	if create {
		if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("workspace directory is not a real directory")
	}
	canonical, err := issueCanonicalPath(path)
	if err != nil || canonical != path {
		return errors.New("workspace directory has a symlinked ancestor")
	}
	return nil
}

func issueOwnerDirectory(common string, create bool) (string, error) {
	path := filepath.Join(common, "factory-issue-workspaces")
	return path, issueSafeDirectory(path, create)
}

func issueReadOwner(common, jobID string) (issueWorkspaceOwner, error) {
	var owner issueWorkspaceOwner
	directory, err := issueOwnerDirectory(common, false)
	if err != nil {
		return owner, err
	}
	path := filepath.Join(directory, jobID+".json")
	info, err := os.Lstat(path)
	if err != nil {
		return owner, err
	}
	if !info.Mode().IsRegular() {
		return owner, errors.New("workspace ownership record is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return owner, err
	}
	if err := json.Unmarshal(data, &owner); err != nil {
		return owner, errors.New("workspace ownership record is invalid")
	}
	if owner.CommonDir != common || owner.Branch != "factory/issue-"+jobID || filepath.Base(owner.Path) != jobID || filepath.Base(filepath.Dir(owner.Path)) != "worktrees" || !filepath.IsAbs(owner.Path) || filepath.Clean(owner.Path) != owner.Path || !issueObjectID(owner.BaseSHA) || owner.HeadSHA != "" && !issueObjectID(owner.HeadSHA) {
		return owner, errors.New("workspace ownership record is inconsistent")
	}
	return owner, nil
}

func issueWriteOwner(common, jobID string, owner issueWorkspaceOwner, create bool) error {
	directory, err := issueOwnerDirectory(common, true)
	if err != nil {
		return err
	}
	data, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, jobID+".json")
	if create {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return fmt.Errorf("reserve workspace ownership: %w", err)
		}
		_, writeErr := file.Write(data)
		syncErr := file.Sync()
		closeErr := file.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return err
		}
	} else {
		file, err := os.CreateTemp(directory, ".owner-")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		_, writeErr := file.Write(data)
		syncErr := file.Sync()
		closeErr := file.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return err
		}
		if err := os.Rename(file.Name(), path); err != nil {
			return err
		}
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func issueWorktrees(ctx context.Context, repositoryPath string) ([]issueWorktreeEntry, error) {
	data, err := issueGit(ctx, repositoryPath, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var entries []issueWorktreeEntry
	var entry issueWorktreeEntry
	for _, field := range strings.Split(string(data), "\x00") {
		if field == "" {
			if entry.Path != "" {
				entries = append(entries, entry)
				entry = issueWorktreeEntry{}
			}
			continue
		}
		key, value, _ := strings.Cut(field, " ")
		switch key {
		case "worktree":
			entry.Path = value
		case "branch":
			entry.Branch = value
		case "HEAD":
			entry.Head = value
		case "locked":
			entry.Locked = true
		}
	}
	return entries, nil
}

func issueCheckAbandoned(ctx context.Context, repositoryPath, common, jobID string) error {
	directory, err := issueOwnerDirectory(common, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	files, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	entries, err := issueWorktrees(ctx, repositoryPath)
	if err != nil {
		return err
	}
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(file.Name(), ".json")
		if !validIssueJobID(id) {
			return errors.New("invalid repository workspace ownership record")
		}
		owner, err := issueReadOwner(common, id)
		if err != nil {
			return err
		}
		if id == jobID || owner.HeadSHA != "" {
			continue
		}
		_, err = os.Lstat(owner.Path)
		if err == nil || !errors.Is(err, os.ErrNotExist) {
			return errors.New("repository has an uncheckpointed issue workspace; manual recovery is required")
		}
		for _, entry := range entries {
			if entry.Path == owner.Path {
				return errors.New("repository has an uncheckpointed registered worktree; manual recovery is required")
			}
		}
	}
	return nil
}

func acquireIssueLease(repositoryPath, jobID string) (func(), error) {
	if !validIssueJobID(jobID) {
		return nil, errors.New("invalid issue workspace job ID")
	}
	ctx := context.Background()
	common, err := issueRepositoryCommonDir(ctx, repositoryPath)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(common, "factory-issue.lock")
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, fmt.Errorf("open repository lease: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("repository lease is not a regular file")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("repository is leased by another issue worker: %w", err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			_ = syscall.Flock(fd, syscall.LOCK_UN)
			_ = file.Close()
		})
	}
	if err := issueCheckAbandoned(ctx, repositoryPath, common, jobID); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func issueObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func issueCheckRemote(ctx context.Context, repositoryPath, remoteURL string) error {
	// Absolute local paths support offline repositories; production callers
	// supply the HTTPS URL pinned to the checked GitHub repository identity.
	if !filepath.IsAbs(remoteURL) {
		remote, err := parseGitHubRemote(remoteURL)
		if err != nil || remoteURL != "https://github.com/"+remote.Owner+"/"+remote.Name+".git" {
			return errors.New("workspace remote must be a canonical GitHub HTTPS URL or absolute local path")
		}
	}
	if strings.ContainsAny(remoteURL, "\r\n\x00") {
		return errors.New("invalid workspace remote")
	}
	data, err := issueGit(ctx, repositoryPath, "config", "--get-regexp", `^url\..*\.(insteadof|pushinsteadof)$`)
	if err != nil && !issueGitExit(err, 1) {
		return err
	}
	if len(data) != 0 {
		return errors.New("Git URL rewrite configuration prevents pinning the issue remote")
	}
	// Git treats an argument as a configured remote name before treating it
	// as a URL. A specially named remote must not redirect this exact URL.
	_, err = issueGit(ctx, repositoryPath, "config", "--get-all", "remote."+remoteURL+".url")
	if err == nil {
		return errors.New("a configured Git remote shadows the pinned issue URL")
	}
	if !issueGitExit(err, 1) {
		return err
	}
	return nil
}

func prepareIssueWorkspace(ctx context.Context, data, repositoryPath, jobID, remoteURL, baseBranch string) (issueWorkspace, error) {
	var w issueWorkspace
	if !validIssueJobID(jobID) {
		return w, errors.New("invalid issue workspace job ID")
	}
	common, err := issueRepositoryCommonDir(ctx, repositoryPath)
	if err != nil {
		return w, err
	}
	if err := issueCheckAbandoned(ctx, repositoryPath, common, jobID); err != nil {
		return w, err
	}
	if err := issueCheckRemote(ctx, repositoryPath, remoteURL); err != nil {
		return w, err
	}
	if _, err := issueGit(ctx, repositoryPath, "check-ref-format", "refs/heads/"+baseBranch); err != nil {
		return w, errors.New("invalid issue base branch")
	}
	if err := os.MkdirAll(data, 0700); err != nil {
		return w, err
	}
	root, err := issueCanonicalPath(data)
	if err != nil {
		return w, err
	}
	parent := filepath.Join(root, "worktrees")
	if err := issueSafeDirectory(parent, true); err != nil {
		return w, err
	}
	w.Path, w.Branch = filepath.Join(parent, jobID), "factory/issue-"+jobID
	if _, err := os.Lstat(w.Path); !errors.Is(err, os.ErrNotExist) {
		return w, errors.New("issue workspace path already exists or cannot be inspected")
	}
	if _, err := issueReadOwner(common, jobID); !errors.Is(err, os.ErrNotExist) {
		return w, errors.New("issue workspace ownership already exists or is ambiguous")
	}
	entries, err := issueWorktrees(ctx, repositoryPath)
	if err != nil {
		return w, err
	}
	if issuePathWithin(w.Path, common) {
		return w, errors.New("issue workspace cannot be inside the Git common directory")
	}
	for _, entry := range entries {
		if issuePathWithin(w.Path, entry.Path) {
			return w, errors.New("issue workspace cannot be inside another checkout of this repository")
		}
		if entry.Path == w.Path || entry.Branch == "refs/heads/"+w.Branch {
			return w, errors.New("issue workspace is already registered")
		}
	}
	baseRef := "refs/factory/issue-bases/" + jobID
	for _, ref := range []string{"refs/heads/" + w.Branch, baseRef} {
		_, err := issueGit(ctx, repositoryPath, "show-ref", "--verify", "--quiet", ref)
		if !issueGitExit(err, 1) {
			return w, errors.New("issue workspace ref already exists or cannot be inspected")
		}
	}
	if _, err := issueGit(ctx, repositoryPath, "fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--", remoteURL, "refs/heads/"+baseBranch+":"+baseRef); err != nil {
		return w, err
	}
	w.BaseSHA, err = issueGitText(ctx, repositoryPath, "rev-parse", "--verify", baseRef+"^{commit}")
	if err != nil || !issueObjectID(w.BaseSHA) {
		return w, errors.New("fetched issue base is not a commit")
	}
	owner := issueWorkspaceOwner{issueWorkspace: w, CommonDir: common}
	if err := issueWriteOwner(common, jobID, owner, true); err != nil {
		return w, err
	}
	if _, err := issueGit(ctx, repositoryPath, "worktree", "add", "--no-track", "-b", w.Branch, "--", w.Path, w.BaseSHA); err != nil {
		return w, err
	}
	owner.GitDir, err = issueGitText(ctx, w.Path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return w, err
	}
	owner.GitDir, err = issueCanonicalPath(owner.GitDir)
	if err != nil {
		return w, err
	}
	if err := issueWriteOwner(common, jobID, owner, false); err != nil {
		return w, err
	}
	if _, err := issueValidateWorkspace(ctx, repositoryPath, w, false); err != nil {
		return w, err
	}
	return w, nil
}

func issuePathWithin(path, parent string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func issueValidateWorkspace(ctx context.Context, repositoryPath string, w issueWorkspace, allowMissing bool) (issueWorkspaceOwner, error) {
	var owner issueWorkspaceOwner
	jobID := strings.TrimPrefix(w.Branch, "factory/issue-")
	if !validIssueJobID(jobID) || w.Branch != "factory/issue-"+jobID || !issueObjectID(w.BaseSHA) || w.HeadSHA != "" && !issueObjectID(w.HeadSHA) {
		return owner, errors.New("invalid issue workspace ownership")
	}
	common, err := issueRepositoryCommonDir(ctx, repositoryPath)
	if err != nil {
		return owner, err
	}
	owner, err = issueReadOwner(common, jobID)
	if err != nil {
		return owner, err
	}
	if owner.Path != w.Path || owner.Branch != w.Branch || owner.BaseSHA != w.BaseSHA || owner.GitDir == "" || filepath.Dir(owner.GitDir) != filepath.Join(common, "worktrees") {
		return owner, errors.New("issue workspace does not match its durable ownership record")
	}
	if err := issueSafeDirectory(filepath.Dir(w.Path), false); err != nil {
		return owner, err
	}
	entries, err := issueWorktrees(ctx, repositoryPath)
	if err != nil {
		return owner, err
	}
	registered := false
	for _, entry := range entries {
		if entry.Path != w.Path {
			if entry.Branch == "refs/heads/"+w.Branch {
				return owner, errors.New("issue branch belongs to another worktree")
			}
			continue
		}
		if registered || entry.Branch != "refs/heads/"+w.Branch || entry.Locked || !issueObjectID(entry.Head) {
			return owner, errors.New("issue worktree registration changed or is locked")
		}
		registered = true
	}
	info, err := os.Lstat(w.Path)
	if errors.Is(err, os.ErrNotExist) && allowMissing {
		if registered {
			if err := issueValidateGitDir(owner); err != nil {
				return owner, err
			}
		}
		return owner, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !registered {
		return owner, errors.New("issue worktree is missing, unregistered, or not a real directory")
	}
	canonical, err := issueCanonicalPath(w.Path)
	if err != nil || canonical != w.Path {
		return owner, errors.New("issue worktree path changed or contains symlinks")
	}
	if err := issueValidateGitDir(owner); err != nil {
		return owner, err
	}
	gitFile, err := os.Lstat(filepath.Join(w.Path, ".git"))
	if err != nil || !gitFile.Mode().IsRegular() {
		return owner, errors.New("issue worktree is not a linked Git checkout")
	}
	actualCommon, err := issueRepositoryCommonDir(ctx, w.Path)
	if err != nil || actualCommon != common {
		return owner, errors.New("issue worktree belongs to a different repository")
	}
	gitDir, err := issueGitText(ctx, w.Path, "rev-parse", "--absolute-git-dir")
	if err != nil || gitDir != owner.GitDir {
		return owner, errors.New("issue worktree administrative directory changed")
	}
	branch, err := issueGitText(ctx, w.Path, "symbolic-ref", "--quiet", "HEAD")
	if err != nil || branch != "refs/heads/"+w.Branch {
		return owner, errors.New("issue worktree branch changed")
	}
	return owner, nil
}

func issueValidateGitDir(owner issueWorkspaceOwner) error {
	if err := issueSafeDirectory(owner.GitDir, false); err != nil {
		return err
	}
	gitFile := filepath.Join(owner.GitDir, "gitdir")
	info, err := os.Lstat(gitFile)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("issue worktree registration pointer is invalid")
	}
	pointer, err := os.ReadFile(gitFile)
	if err != nil || strings.TrimSpace(string(pointer)) != filepath.Join(owner.Path, ".git") {
		return errors.New("issue worktree registration points elsewhere")
	}
	return nil
}

func issueCheckIndex(ctx context.Context, path string) error {
	data, err := issueGit(ctx, path, "ls-files", "-v", "-z")
	if err != nil {
		return err
	}
	for _, file := range strings.Split(string(data), "\x00") {
		if file != "" && (file[0] == 'S' || file[0] >= 'a' && file[0] <= 'z') {
			return errors.New("issue index hides changes with assume-unchanged or skip-worktree")
		}
	}
	return nil
}

func issueRequireClean(ctx context.Context, path string) error {
	if err := issueCheckIndex(ctx, path); err != nil {
		return err
	}
	status, err := issueGit(ctx, path, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	if len(status) != 0 {
		return errors.New("issue workspace contains uncheckpointed changes; preserving it")
	}
	return nil
}

func issueRequireCandidate(ctx context.Context, path string, w issueWorkspace) error {
	head, err := issueGitText(ctx, path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || head != w.HeadSHA || !issueObjectID(w.HeadSHA) {
		return errors.New("issue workspace no longer matches its checkpoint")
	}
	if _, err := issueGit(ctx, path, "merge-base", "--is-ancestor", w.BaseSHA, w.HeadSHA); err != nil {
		return errors.New("issue checkpoint is not descended from its fetched base")
	}
	_, err = issueGit(ctx, path, "diff", "--no-ext-diff", "--no-textconv", "--quiet", w.BaseSHA, w.HeadSHA, "--")
	if !issueGitExit(err, 1) {
		return errors.New("issue checkpoint has no publishable diff against its base")
	}
	return issueRequireClean(ctx, path)
}

// Hooks are deliberately disabled, including Git LFS's upload hook. Do not
// publish a pointer whose external binary object Factory has not uploaded.
func issueRejectLFSCandidate(ctx context.Context, path, base string) error {
	changed, err := issueGit(ctx, path, "diff", "--cached", "--name-only", "-z", "--diff-filter=ACMRT", base, "--")
	if err != nil {
		return err
	}
	for _, name := range strings.Split(string(changed), "\x00") {
		if name == "" {
			continue
		}
		object := ":" + name
		sizeText, err := issueGitText(ctx, path, "cat-file", "-s", object)
		if err != nil {
			return err
		}
		size, err := strconv.ParseInt(sizeText, 10, 64)
		if err != nil {
			return err
		}
		if size > 1024 {
			continue
		}
		content, err := issueGit(ctx, path, "cat-file", "blob", object)
		if err != nil {
			return err
		}
		if strings.HasPrefix(string(content), "version https://git-lfs.github.com/spec/v1\n") {
			return errors.New("candidate changes a Git LFS pointer; external LFS object publication is unsupported, so the workspace is retained")
		}
	}
	return nil
}

func checkpointIssueWorkspace(ctx context.Context, repositoryPath string, w issueWorkspace, message string) (issueWorkspace, error) {
	owner, err := issueValidateWorkspace(ctx, repositoryPath, w, false)
	if err != nil {
		return w, err
	}
	if w.HeadSHA != "" {
		head, err := issueGitText(ctx, w.Path, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil || head != w.HeadSHA || owner.HeadSHA != w.HeadSHA {
			return w, errors.New("issue checkpoint changed before checkpointing")
		}
	}
	if err := issueCheckIndex(ctx, w.Path); err != nil {
		return w, err
	}
	if _, err := issueGit(ctx, w.Path, "add", "--all", "--", "."); err != nil {
		return w, err
	}
	if err := issueRejectLFSCandidate(ctx, w.Path, w.BaseSHA); err != nil {
		return w, err
	}
	_, err = issueGit(ctx, w.Path, "diff", "--no-ext-diff", "--no-textconv", "--cached", "--quiet", "--")
	if issueGitExit(err, 1) {
		if _, err := issueGit(ctx, w.Path, "-c", "user.name=Factory", "-c", "user.email=factory@localhost", "commit", "--no-verify", "-m", message); err != nil {
			return w, err
		}
	} else if err != nil {
		return w, err
	}
	w.HeadSHA, err = issueGitText(ctx, w.Path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return w, err
	}
	if _, err := issueValidateWorkspace(ctx, repositoryPath, w, false); err != nil {
		return w, err
	}
	if err := issueRequireCandidate(ctx, w.Path, w); err != nil {
		w.HeadSHA = ""
		return w, err
	}
	owner.HeadSHA = w.HeadSHA
	if err := issueWriteOwner(owner.CommonDir, strings.TrimPrefix(w.Branch, "factory/issue-"), owner, false); err != nil {
		return w, err
	}
	return w, nil
}

func pushIssueWorkspace(ctx context.Context, repositoryPath string, w issueWorkspace, remoteURL string) error {
	owner, err := issueValidateWorkspace(ctx, repositoryPath, w, false)
	if err != nil {
		return err
	}
	if owner.HeadSHA != w.HeadSHA {
		return errors.New("issue checkpoint does not match its durable ownership record")
	}
	if err := issueRequireCandidate(ctx, w.Path, w); err != nil {
		return err
	}
	if err := issueCheckRemote(ctx, repositoryPath, remoteURL); err != nil {
		return err
	}
	ref := "refs/heads/" + w.Branch
	output, err := issueGitText(ctx, repositoryPath, "ls-remote", "--refs", "--", remoteURL, ref)
	if err != nil {
		return err
	}
	if output != "" {
		if output == w.HeadSHA+"\t"+ref {
			return nil
		}
		return errors.New("remote issue branch already exists with a different commit")
	}
	// An empty expected ref is an atomic create-only comparison, NOT permission
	// to overwrite an existing branch. A preflight plus plain push could race
	// with someone creating an ancestor and silently fast-forward their branch.
	_, err = issueGit(ctx, repositoryPath, "-c", "push.followTags=false", "push", "--porcelain", "--no-verify", "--recurse-submodules=no", "--force-with-lease="+ref+":", "--", remoteURL, w.HeadSHA+":"+ref)
	return err
}

// A revision advances only the PR head observed at admission. The explicit
// lease is enforced by the remote even if another writer races the preflight.
func pushReviewWorkspace(ctx context.Context, repositoryPath string, w issueWorkspace, remoteURL, branch, expected string) error {
	owner, err := issueValidateWorkspace(ctx, repositoryPath, w, false)
	if err != nil {
		return err
	}
	if owner.HeadSHA != w.HeadSHA || w.BaseSHA != expected || !issueObjectID(expected) {
		return errors.New("revision checkpoint does not match its observed PR head")
	}
	if err := issueRequireCandidate(ctx, w.Path, w); err != nil {
		return err
	}
	if err := issueCheckRemote(ctx, repositoryPath, remoteURL); err != nil {
		return err
	}
	if _, err := issueGit(ctx, repositoryPath, "check-ref-format", "refs/heads/"+branch); err != nil {
		return errors.New("invalid PR head branch")
	}
	ref := "refs/heads/" + branch
	output, err := issueGitText(ctx, repositoryPath, "ls-remote", "--refs", "--", remoteURL, ref)
	if err != nil {
		return err
	}
	if output != expected+"\t"+ref {
		return errors.New("PR branch changed during revision; refusing to overwrite it")
	}
	_, err = issueGit(ctx, repositoryPath, "-c", "push.followTags=false", "push", "--porcelain", "--no-verify", "--recurse-submodules=no", "--force-with-lease="+ref+":"+expected, "--", remoteURL, w.HeadSHA+":"+ref)
	return err
}

func cleanupIssueWorkspace(ctx context.Context, data, repositoryPath, jobID string, w issueWorkspace) error {
	if !validIssueJobID(jobID) || w.Branch != "factory/issue-"+jobID {
		return errors.New("invalid issue cleanup ownership")
	}
	root, err := issueCanonicalPath(data)
	if err != nil {
		return err
	}
	if w.Path != filepath.Join(root, "worktrees", jobID) {
		return errors.New("refusing cleanup outside the owned issue workspace")
	}
	owner, err := issueValidateWorkspace(ctx, repositoryPath, w, true)
	if err != nil {
		return err
	}
	if !issueObjectID(w.HeadSHA) || owner.HeadSHA != w.HeadSHA {
		return errors.New("refusing cleanup of an uncheckpointed issue workspace")
	}
	branchHead, err := issueGitText(ctx, repositoryPath, "rev-parse", "--verify", "refs/heads/"+w.Branch+"^{commit}")
	if err != nil || branchHead != w.HeadSHA {
		return errors.New("issue branch no longer preserves its checkpoint")
	}
	if _, err := os.Lstat(w.Path); errors.Is(err, os.ErrNotExist) {
		entries, err := issueWorktrees(ctx, repositoryPath)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Path == w.Path {
				_, err := issueGit(ctx, repositoryPath, "worktree", "remove", "--", w.Path)
				return err
			}
		}
		return nil
	}
	if err := issueRequireCandidate(ctx, w.Path, w); err != nil {
		return err
	}
	if _, err := issueGit(ctx, repositoryPath, "worktree", "remove", "--", w.Path); err == nil {
		return nil
	}
	// Git versions differ on whether ignored build outputs block removal.
	// Force is permitted only for observed ignored files in the exact owned
	// checkout after another complete ownership and clean-checkpoint check.
	if _, err := issueValidateWorkspace(ctx, repositoryPath, w, false); err != nil {
		return err
	}
	if err := issueRequireCandidate(ctx, w.Path, w); err != nil {
		return err
	}
	ignored, err := issueGit(ctx, w.Path, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil || len(ignored) == 0 {
		return errors.New("Git refused safe worktree removal; preserving the workspace")
	}
	staged, err := issueGit(ctx, w.Path, "ls-files", "--stage", "-z")
	if err != nil {
		return err
	}
	for _, entry := range strings.Split(string(staged), "\x00") {
		if strings.HasPrefix(entry, "160000 ") {
			return errors.New("refusing forced removal of a worktree containing submodules")
		}
	}
	_, err = issueGit(ctx, repositoryPath, "worktree", "remove", "--force", "--", w.Path)
	return err
}

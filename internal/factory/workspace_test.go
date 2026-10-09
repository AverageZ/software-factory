package factory

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type workspaceFixture struct {
	repository string
	remote     string
	data       string
	base       string
}

func workspaceTestGit(t *testing.T, path string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
	for _, setting := range os.Environ() {
		key, _, _ := strings.Cut(setting, "=")
		if !strings.HasPrefix(key, "GIT_") {
			cmd.Env = append(cmd.Env, setting)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func workspaceTestWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func newWorkspaceFixture(t *testing.T) workspaceFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for real workspace integration coverage")
	}
	root, err := issueCanonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	f := workspaceFixture{
		repository: filepath.Join(root, "developer"),
		remote:     filepath.Join(root, "remote.git"),
		data:       filepath.Join(root, "daemon"),
	}
	workspaceTestGit(t, root, "init", "--bare", "--initial-branch=main", f.remote)
	workspaceTestGit(t, root, "init", "--initial-branch=main", f.repository)
	workspaceTestGit(t, f.repository, "config", "user.name", "Developer")
	workspaceTestGit(t, f.repository, "config", "user.email", "developer@example.invalid")
	workspaceTestGit(t, f.repository, "config", "commit.gpgSign", "false")
	workspaceTestWrite(t, filepath.Join(f.repository, "tracked.txt"), []byte("base\n"))
	workspaceTestWrite(t, filepath.Join(f.repository, ".gitignore"), []byte("dependencies/\nbuild/\n"))
	workspaceTestGit(t, f.repository, "add", ".")
	workspaceTestGit(t, f.repository, "commit", "-m", "base")
	workspaceTestGit(t, f.repository, "remote", "add", "origin", f.remote)
	workspaceTestGit(t, f.repository, "push", "origin", "main")
	f.base = workspaceTestGit(t, f.repository, "rev-parse", "HEAD")
	return f
}

func workspaceTestPrepare(t *testing.T, f workspaceFixture, id string) issueWorkspace {
	t.Helper()
	release, err := acquireIssueLease(f.repository, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	w, err := prepareIssueWorkspace(context.Background(), f.data, f.repository, id, f.remote, "main")
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func workspaceTestCheckpoint(t *testing.T, f workspaceFixture, w issueWorkspace) issueWorkspace {
	t.Helper()
	workspaceTestWrite(t, filepath.Join(w.Path, "tracked.txt"), []byte("issue repair\n"))
	result, err := checkpointIssueWorkspace(context.Background(), f.repository, w, "Repair issue")
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestIssueWorkspaceIsolationFreshBaseAndGitEnvironment(t *testing.T) {
	f := newWorkspaceFixture(t)
	upstream := filepath.Join(filepath.Dir(f.remote), "upstream")
	workspaceTestGit(t, filepath.Dir(upstream), "clone", f.remote, upstream)
	workspaceTestGit(t, upstream, "config", "user.name", "Upstream")
	workspaceTestGit(t, upstream, "config", "user.email", "upstream@example.invalid")
	workspaceTestWrite(t, filepath.Join(upstream, "fresh.txt"), []byte("new remote base\n"))
	workspaceTestGit(t, upstream, "add", ".")
	workspaceTestGit(t, upstream, "-c", "commit.gpgSign=false", "commit", "-m", "remote advancement")
	workspaceTestGit(t, upstream, "push", "origin", "main")
	freshBase := workspaceTestGit(t, upstream, "rev-parse", "HEAD")
	workspaceTestWrite(t, filepath.Join(f.repository, "tracked.txt"), []byte("staged developer work\n"))
	workspaceTestGit(t, f.repository, "add", "tracked.txt")
	workspaceTestWrite(t, filepath.Join(f.repository, "tracked.txt"), []byte("unstaged developer work\n"))
	workspaceTestWrite(t, filepath.Join(f.repository, "developer-only.txt"), []byte("private developer work"))
	beforeStatus := workspaceTestGit(t, f.repository, "status", "--porcelain=v1")
	beforeIndex := workspaceTestGit(t, f.repository, "write-tree")
	gitDir := workspaceTestGit(t, f.repository, "rev-parse", "--absolute-git-dir")
	workspaceTestWrite(t, filepath.Join(gitDir, "FETCH_HEAD"), []byte("developer fetch sentinel\n"))
	foreignIndex := filepath.Join(filepath.Dir(f.remote), "foreign-index")
	workspaceTestWrite(t, foreignIndex, []byte("must remain untouched"))
	t.Setenv("GIT_DIR", filepath.Join(upstream, ".git"))
	t.Setenv("GIT_COMMON_DIR", filepath.Join(upstream, ".git"))
	t.Setenv("GIT_WORK_TREE", upstream)
	t.Setenv("GIT_INDEX_FILE", foreignIndex)
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(upstream, ".git", "objects"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.worktree")
	t.Setenv("GIT_CONFIG_VALUE_0", upstream)
	w := workspaceTestPrepare(t, f, "fresh-base")
	if w.BaseSHA != freshBase || w.HeadSHA != "" {
		t.Fatalf("prepared state = %#v; want fresh base %s and no checkpoint", w, freshBase)
	}
	if got := workspaceTestGit(t, w.Path, "rev-parse", "HEAD"); got != freshBase {
		t.Fatalf("worktree HEAD = %s, want %s", got, freshBase)
	}
	w = workspaceTestCheckpoint(t, f, w)
	if got := workspaceTestGit(t, f.repository, "status", "--porcelain=v1"); got != beforeStatus {
		t.Fatalf("developer status changed: %q -> %q", beforeStatus, got)
	}
	if got := workspaceTestGit(t, f.repository, "write-tree"); got != beforeIndex {
		t.Fatalf("developer index changed: %s -> %s", beforeIndex, got)
	}
	if got := workspaceTestGit(t, f.repository, "rev-parse", "HEAD"); got != f.base {
		t.Fatalf("developer branch advanced: %s", got)
	}
	for path, want := range map[string]string{
		foreignIndex:                        "must remain untouched",
		filepath.Join(gitDir, "FETCH_HEAD"): "developer fetch sentinel\n",
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("unrelated Git state %s changed: %q, %v", path, got, err)
		}
	}
	if err := cleanupIssueWorkspace(context.Background(), f.data, f.repository, "fresh-base", w); err != nil {
		t.Fatal(err)
	}
}

func TestIssueWorkspaceLeaseExcludesOtherDataDirectoriesAndAbandonedWork(t *testing.T) {
	f := newWorkspaceFixture(t)
	release, err := acquireIssueLease(f.repository, "first")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if other, err := acquireIssueLease(f.repository, "second"); err == nil {
		other()
		t.Fatal("second writer acquired the repository-wide lease")
	}
	w, err := prepareIssueWorkspace(context.Background(), f.data, f.repository, "first", f.remote, "main")
	if err != nil {
		t.Fatal(err)
	}
	if other, err := acquireIssueLease(w.Path, "second"); err == nil {
		other()
		t.Fatal("linked checkout bypassed the common-directory lease")
	}
	release()
	release()
	if other, err := acquireIssueLease(f.repository, "second"); err == nil {
		other()
		t.Fatal("abandoned uncheckpointed work allowed a new daemon writer")
	}
	otherData := filepath.Join(filepath.Dir(f.data), "other-daemon")
	if _, err := prepareIssueWorkspace(context.Background(), otherData, f.repository, "second", f.remote, "main"); err == nil {
		t.Fatal("another data directory bypassed abandoned workspace ownership")
	}
	resume, err := acquireIssueLease(f.repository, "first")
	if err != nil {
		t.Fatalf("same-job resume cannot acquire lease: %v", err)
	}
	w = workspaceTestCheckpoint(t, f, w)
	resume()
	other, err := acquireIssueLease(f.repository, "second")
	if err != nil {
		t.Fatalf("durable checkpoint did not release abandoned-work exclusion: %v", err)
	}
	other()
	if _, err := prepareIssueWorkspace(context.Background(), otherData, f.repository, "first", f.remote, "main"); err == nil {
		t.Fatal("same ID in another data directory replaced an owned branch")
	}
}

func TestIssueWorkspaceCheckpointIncludesBinaryUntrackedAndDeletion(t *testing.T) {
	f := newWorkspaceFixture(t)
	w := workspaceTestPrepare(t, f, "binary")
	binary := []byte{0, 1, 255, 0, 42, 128}
	workspaceTestWrite(t, filepath.Join(w.Path, "assets", "binary.dat"), binary)
	workspaceTestWrite(t, filepath.Join(w.Path, "new file\nwith newline.txt"), []byte("new untracked file\n"))
	workspaceTestWrite(t, filepath.Join(w.Path, "dependencies", "ignored.dat"), []byte("large generated dependency"))
	if err := os.Remove(filepath.Join(w.Path, "tracked.txt")); err != nil {
		t.Fatal(err)
	}
	w, err := checkpointIssueWorkspace(context.Background(), f.repository, w, "Binary repair")
	if err != nil {
		t.Fatal(err)
	}
	got, err := issueGit(context.Background(), w.Path, "show", w.HeadSHA+":assets/binary.dat")
	if err != nil || !bytes.Equal(got, binary) {
		t.Fatalf("checkpoint binary = %v, %v", got, err)
	}
	if got := workspaceTestGit(t, w.Path, "show", w.HeadSHA+":new file\nwith newline.txt"); got != "new untracked file" {
		t.Fatalf("untracked file was not checkpointed: %q", got)
	}
	for _, name := range []string{"tracked.txt", "dependencies/ignored.dat"} {
		if _, err := issueGit(context.Background(), w.Path, "cat-file", "-e", w.HeadSHA+":"+name); err == nil {
			t.Fatalf("unwanted file %s exists in checkpoint", name)
		}
	}
	if err := pushIssueWorkspace(context.Background(), f.repository, w, f.remote); err != nil {
		t.Fatal(err)
	}
	if err := pushIssueWorkspace(context.Background(), f.repository, w, f.remote); err != nil {
		t.Fatalf("exact checkpoint push is not idempotent: %v", err)
	}
	if got := workspaceTestGit(t, f.remote, "rev-parse", "refs/heads/"+w.Branch); got != w.HeadSHA {
		t.Fatalf("remote has %s, want exact checkpoint %s", got, w.HeadSHA)
	}
	if err := cleanupIssueWorkspace(context.Background(), f.data, f.repository, "binary", w); err != nil {
		t.Fatalf("ignored dependencies prevented safe published cleanup: %v", err)
	}
	if _, err := os.Lstat(w.Path); !os.IsNotExist(err) {
		t.Fatalf("published workspace still exists: %v", err)
	}
	if got := workspaceTestGit(t, f.repository, "rev-parse", "refs/heads/"+w.Branch); got != w.HeadSHA {
		t.Fatalf("cleanup discarded checkpoint branch: %s", got)
	}
	if err := cleanupIssueWorkspace(context.Background(), f.data, f.repository, "binary", w); err != nil {
		t.Fatalf("cleanup is not idempotent: %v", err)
	}
}

func TestIssueWorkspaceCleanupPreservesDirtyAndHiddenChanges(t *testing.T) {
	for _, mode := range []string{"tracked", "untracked", "assume-unchanged", "skip-worktree"} {
		t.Run(mode, func(t *testing.T) {
			f := newWorkspaceFixture(t)
			w := workspaceTestCheckpoint(t, f, workspaceTestPrepare(t, f, "dirty"))
			path := filepath.Join(w.Path, "tracked.txt")
			if mode == "untracked" {
				path = filepath.Join(w.Path, "untracked.txt")
			}
			if mode == "assume-unchanged" || mode == "skip-worktree" {
				workspaceTestGit(t, w.Path, "update-index", "--"+mode, "tracked.txt")
			}
			workspaceTestWrite(t, path, []byte("human work after checkpoint\n"))
			workspaceTestWrite(t, filepath.Join(w.Path, "build", "ignored-output"), []byte("generated"))
			if err := cleanupIssueWorkspace(context.Background(), f.data, f.repository, "dirty", w); err == nil {
				t.Fatal("cleanup accepted uncheckpointed work")
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != "human work after checkpoint\n" {
				t.Fatalf("cleanup lost human work: %q, %v", got, err)
			}
			if err := pushIssueWorkspace(context.Background(), f.repository, w, f.remote); err == nil {
				t.Fatal("publication accepted uncheckpointed work")
			}
		})
	}
}

func TestIssueWorkspaceRejectsEmptyDiffAndChangedBranch(t *testing.T) {
	f := newWorkspaceFixture(t)
	w := workspaceTestPrepare(t, f, "empty")
	if result, err := checkpointIssueWorkspace(context.Background(), f.repository, w, "No changes"); err == nil || result.HeadSHA != "" {
		t.Fatalf("empty change produced a candidate: %#v, %v", result, err)
	}
	workspaceTestGit(t, w.Path, "switch", "-c", "human-branch")
	workspaceTestWrite(t, filepath.Join(w.Path, "tracked.txt"), []byte("human branch work"))
	if _, err := checkpointIssueWorkspace(context.Background(), f.repository, w, "Wrong branch"); err == nil {
		t.Fatal("checkpoint accepted a changed branch")
	}
	if err := cleanupIssueWorkspace(context.Background(), f.data, f.repository, "empty", w); err == nil {
		t.Fatal("cleanup accepted a changed branch")
	}
	if got := workspaceTestGit(t, w.Path, "diff", "--", "tracked.txt"); !strings.Contains(got, "human branch work") {
		t.Fatal("changed branch work was not preserved")
	}
}

func TestIssueWorkspaceRejectsInvalidOwnershipAndSymlinks(t *testing.T) {
	f := newWorkspaceFixture(t)
	w := workspaceTestCheckpoint(t, f, workspaceTestPrepare(t, f, "owned"))
	other := filepath.Join(filepath.Dir(f.data), "human-worktree")
	workspaceTestGit(t, f.repository, "worktree", "add", "-b", "human", other, "main")
	for _, path := range []string{f.repository, other, filepath.Join(w.Path, "..", "owned")} {
		// Join normalizes traversal, so explicitly preserve it for this case.
		if path == w.Path {
			path = w.Path + "/../owned"
		}
		forged := w
		forged.Path = path
		if err := cleanupIssueWorkspace(context.Background(), f.data, f.repository, "owned", forged); err == nil {
			t.Fatalf("cleanup accepted foreign or noncanonical path %s", path)
		}
		if _, err := checkpointIssueWorkspace(context.Background(), f.repository, forged, "Forged"); err == nil {
			t.Fatalf("checkpoint accepted foreign or noncanonical path %s", path)
		}
	}
	if err := cleanupIssueWorkspace(context.Background(), filepath.Dir(f.data), f.repository, "owned", w); err == nil {
		t.Fatal("cleanup accepted a different daemon data root")
	}
	moved := w.Path + "-preserved"
	if err := os.Rename(w.Path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, w.Path); err != nil {
		t.Fatal(err)
	}
	if err := cleanupIssueWorkspace(context.Background(), f.data, f.repository, "owned", w); err == nil {
		t.Fatal("cleanup followed a substituted worktree symlink")
	}
	if _, err := os.Stat(filepath.Join(other, "tracked.txt")); err != nil {
		t.Fatalf("foreign checkout was damaged: %v", err)
	}
	if _, err := prepareIssueWorkspace(context.Background(), f.data, f.repository, "../escape", f.remote, "main"); err == nil {
		t.Fatal("workspace accepted a traversal job ID")
	}
}

func TestIssueWorkspaceCleanupMissingRegisteredWorktree(t *testing.T) {
	f := newWorkspaceFixture(t)
	w := workspaceTestCheckpoint(t, f, workspaceTestPrepare(t, f, "missing"))
	other := filepath.Join(filepath.Dir(f.data), "unrelated")
	workspaceTestGit(t, f.repository, "worktree", "add", "-b", "unrelated", other, "main")
	if err := os.RemoveAll(w.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(other); err != nil {
		t.Fatal(err)
	}
	if err := cleanupIssueWorkspace(context.Background(), f.data, f.repository, "missing", w); err != nil {
		t.Fatal(err)
	}
	entries, err := issueWorktrees(context.Background(), f.repository)
	if err != nil {
		t.Fatal(err)
	}
	foundOther := false
	for _, entry := range entries {
		if entry.Path == w.Path {
			t.Fatal("missing owned worktree registration was not removed")
		}
		if entry.Path == other {
			foundOther = true
		}
	}
	if !foundOther {
		t.Fatal("targeted cleanup pruned another missing worktree")
	}
	if err := cleanupIssueWorkspace(context.Background(), f.data, f.repository, "missing", w); err != nil {
		t.Fatalf("missing-worktree cleanup is not idempotent: %v", err)
	}
}

func TestIssueWorkspaceRemoteCollisionAndExplicitDestination(t *testing.T) {
	f := newWorkspaceFixture(t)
	w := workspaceTestCheckpoint(t, f, workspaceTestPrepare(t, f, "collision"))
	workspaceTestGit(t, f.remote, "update-ref", "refs/heads/"+w.Branch, f.base)
	if err := pushIssueWorkspace(context.Background(), f.repository, w, f.remote); err == nil {
		t.Fatal("publication overwrote an existing ancestor branch")
	}
	if got := workspaceTestGit(t, f.remote, "rev-parse", "refs/heads/"+w.Branch); got != f.base {
		t.Fatalf("colliding remote branch changed to %s", got)
	}
	workspaceTestGit(t, f.remote, "update-ref", "-d", "refs/heads/"+w.Branch)
	decoy := filepath.Join(filepath.Dir(f.remote), "decoy.git")
	workspaceTestGit(t, filepath.Dir(decoy), "init", "--bare", decoy)
	workspaceTestGit(t, f.repository, "config", "remote.origin.pushurl", decoy)
	if err := pushIssueWorkspace(context.Background(), f.repository, w, f.remote); err != nil {
		t.Fatal(err)
	}
	if got := workspaceTestGit(t, f.remote, "rev-parse", "refs/heads/"+w.Branch); got != w.HeadSHA {
		t.Fatalf("explicit remote did not receive checkpoint: %s", got)
	}
	if output := workspaceTestGit(t, decoy, "for-each-ref", "--format=%(refname)"); output != "" {
		t.Fatalf("origin pushurl received unintended refs: %s", output)
	}
	workspaceTestGit(t, f.repository, "config", "url."+decoy+".pushInsteadOf", f.remote)
	if err := pushIssueWorkspace(context.Background(), f.repository, w, f.remote); err == nil {
		t.Fatal("publication accepted a silently rewritten remote")
	}
	workspaceTestGit(t, f.repository, "config", "--unset-all", "url."+decoy+".pushInsteadOf")
	workspaceTestGit(t, f.repository, "config", "remote."+f.remote+".url", decoy)
	if err := pushIssueWorkspace(context.Background(), f.repository, w, f.remote); err == nil {
		t.Fatal("publication accepted a configured remote shadowing the explicit URL")
	}
}

func TestIssueWorkspacePlacementAndExistingOwnership(t *testing.T) {
	for _, mode := range []string{"inside-checkout", "inside-common-dir", "inside-linked-checkout", "symlink-parent", "existing-path", "existing-branch"} {
		t.Run(mode, func(t *testing.T) {
			f := newWorkspaceFixture(t)
			ctx := context.Background()
			switch mode {
			case "inside-checkout":
				f.data = filepath.Join(f.repository, "daemon-data")
			case "inside-common-dir":
				f.data = filepath.Join(f.repository, ".git", "daemon-data")
			case "inside-linked-checkout":
				linked := filepath.Join(filepath.Dir(f.data), "linked")
				workspaceTestGit(t, f.repository, "worktree", "add", "-b", "linked", linked, "main")
				f.data = filepath.Join(linked, "daemon-data")
			case "symlink-parent":
				if err := os.MkdirAll(f.data, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(f.repository, filepath.Join(f.data, "worktrees")); err != nil {
					t.Fatal(err)
				}
			case "existing-path":
				workspaceTestWrite(t, filepath.Join(f.data, "worktrees", "collision", "human.txt"), []byte("human data"))
			case "existing-branch":
				workspaceTestGit(t, f.repository, "branch", "factory/issue-collision", "main")
			}
			if _, err := prepareIssueWorkspace(ctx, f.data, f.repository, "collision", f.remote, "main"); err == nil {
				t.Fatalf("prepare accepted unsafe placement or ownership: %s", mode)
			}
			if got := workspaceTestGit(t, f.repository, "rev-parse", "HEAD"); got != f.base {
				t.Fatalf("failed preparation changed developer HEAD to %s", got)
			}
			if mode == "existing-path" {
				path := filepath.Join(f.data, "worktrees", "collision", "human.txt")
				if data, err := os.ReadFile(path); err != nil || string(data) != "human data" {
					t.Fatalf("existing worktree contents changed: %q, %v", data, err)
				}
			}
			if mode == "existing-branch" {
				if got := workspaceTestGit(t, f.repository, "rev-parse", "factory/issue-collision"); got != f.base {
					t.Fatalf("existing branch changed to %s", got)
				}
			}
		})
	}
}

func TestIssueWorkspaceAllowsDataInsideUnrelatedCheckout(t *testing.T) {
	f := newWorkspaceFixture(t)
	unrelated := filepath.Join(filepath.Dir(f.data), "factory-checkout")
	workspaceTestGit(t, filepath.Dir(unrelated), "init", "--initial-branch=main", unrelated)
	f.data = filepath.Join(unrelated, ".factory-data")
	w := workspaceTestCheckpoint(t, f, workspaceTestPrepare(t, f, "unrelated-data"))
	if err := cleanupIssueWorkspace(context.Background(), f.data, f.repository, "unrelated-data", w); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(unrelated, ".git")); err != nil {
		t.Fatalf("cleanup removed unrelated checkout: %v", err)
	}
}

func TestIssueWorkspaceMissingUncheckpointedRegistrationBlocksOtherDaemon(t *testing.T) {
	f := newWorkspaceFixture(t)
	w, err := prepareIssueWorkspace(context.Background(), f.data, f.repository, "interrupted", f.remote, "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(w.Path); err != nil {
		t.Fatal(err)
	}
	if release, err := acquireIssueLease(f.repository, "another"); err == nil {
		release()
		t.Fatal("missing but registered uncheckpointed workspace did not block a new writer")
	}
	if err := cleanupIssueWorkspace(context.Background(), f.data, f.repository, "interrupted", w); err == nil {
		t.Fatal("cleanup silently discarded uncheckpointed ownership")
	}
}

func TestIssueWorkspaceCheckpointsExistingAgentCommitsAndRejectsLaterHEAD(t *testing.T) {
	f := newWorkspaceFixture(t)
	w := workspaceTestPrepare(t, f, "agent-commit")
	workspaceTestWrite(t, filepath.Join(w.Path, "tracked.txt"), []byte("agent commit\n"))
	workspaceTestGit(t, w.Path, "add", ".")
	workspaceTestGit(t, w.Path, "commit", "-m", "Agent implemented repair")
	agentHead := workspaceTestGit(t, w.Path, "rev-parse", "HEAD")
	w, err := checkpointIssueWorkspace(context.Background(), f.repository, w, "Checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	if w.HeadSHA != agentHead {
		t.Fatalf("checkpoint replaced the existing agent commit: got %s, want %s", w.HeadSHA, agentHead)
	}
	workspaceTestWrite(t, filepath.Join(w.Path, "tracked.txt"), []byte("human commit after checkpoint\n"))
	workspaceTestGit(t, w.Path, "add", ".")
	workspaceTestGit(t, w.Path, "commit", "-m", "Human change")
	humanHead := workspaceTestGit(t, w.Path, "rev-parse", "HEAD")
	if err := pushIssueWorkspace(context.Background(), f.repository, w, f.remote); err == nil {
		t.Fatal("publication accepted a different HEAD than its checkpoint")
	}
	if err := cleanupIssueWorkspace(context.Background(), f.data, f.repository, "agent-commit", w); err == nil {
		t.Fatal("cleanup removed a workspace whose HEAD advanced after checkpoint")
	}
	if got := workspaceTestGit(t, w.Path, "rev-parse", "HEAD"); got != humanHead {
		t.Fatalf("human commit was changed: %s", got)
	}
}

func TestIssueWorkspaceRetainsUnpublishedLFSObjects(t *testing.T) {
	f := newWorkspaceFixture(t)
	w := workspaceTestPrepare(t, f, "lfs-candidate")
	pointer := []byte("version https://git-lfs.github.com/spec/v1\noid sha256:1234567890123456789012345678901234567890123456789012345678901234\nsize 123456\n")
	path := filepath.Join(w.Path, "binary.lfs")
	workspaceTestWrite(t, path, pointer)
	result, err := checkpointIssueWorkspace(context.Background(), f.repository, w, "LFS change")
	if err == nil || !strings.Contains(err.Error(), "Git LFS") || result.HeadSHA != "" {
		t.Fatalf("external LFS content accepted without upload: %+v, %v", result, err)
	}
	if content, err := os.ReadFile(path); err != nil || !bytes.Equal(content, pointer) {
		t.Fatalf("unsupported binary work was lost: %q, %v", content, err)
	}
	if err := cleanupIssueWorkspace(context.Background(), f.data, f.repository, "lfs-candidate", result); err == nil {
		t.Fatal("unpublished LFS work was cleaned up")
	}
}

package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@t.t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	return dir
}

// gitIn runs a git command in dir, failing the test on error.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
}

// TestRemoveKeepsCommittedWork is the regression for the data-loss path: a
// worktree whose working tree is clean because the agent committed into it must
// NOT lose those commits when the sandbox is cleaned up. Non-force Remove drops
// the checkout but keeps the branch (branchKept), so the commits survive.
func TestRemoveKeepsCommittedWork(t *testing.T) {
	repo := initRepo(t)
	dir, branch, err := Create(repo, "feat")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Agent commits inside the worktree; the working tree is then clean.
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("important\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-m", "agent work")
	if changed, _ := HasChanges(dir); changed {
		t.Fatal("working tree should be clean after commit")
	}

	branchKept, err := Remove(repo, dir, branch, false)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !branchKept {
		t.Error("branch with unmerged commits must be kept, not deleted")
	}
	// The checkout is gone but the branch (and its commit) survives.
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("checkout dir should be removed")
	}
	cmd := exec.Command("git", "-C", repo, "rev-parse", "--verify", branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("branch %s should still exist after Remove, got: %s", branch, out)
	}
}

// TestCreateOrReuseReclaimsWorktree confirms a real existing worktree is reused
// (the wake-reclaim path), returning the same dir and branch.
func TestCreateOrReuseReclaimsWorktree(t *testing.T) {
	repo := initRepo(t)
	dir1, br1, err := Create(repo, "feat")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	dir2, br2, err := CreateOrReuse(repo, "feat")
	if err != nil {
		t.Fatalf("CreateOrReuse should reclaim an existing worktree: %v", err)
	}
	if dir2 != dir1 || br2 != br1 {
		t.Errorf("reclaim mismatch: got (%s,%s), want (%s,%s)", dir2, br2, dir1, br1)
	}
}

// TestCreateOrReuseRejectsNonWorktree confirms a leftover plain directory (e.g.
// from a half-failed create) is not silently treated as a sandbox.
func TestCreateOrReuseRejectsNonWorktree(t *testing.T) {
	repo := initRepo(t)
	ghost := Dir(repo, "ghost")
	if err := os.MkdirAll(ghost, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CreateOrReuse(repo, "ghost"); err == nil {
		t.Error("CreateOrReuse must reject a directory that is not a registered worktree")
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Feature X": "feature-x",
		"":          "scratch",
		"  a/b  ":   "a-b",
		"--Hi--":    "hi",
		"keep.it":   "keep.it",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCreateChangesRemove(t *testing.T) {
	repo := initRepo(t)

	dir, branch, err := Create(repo, "feat")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("worktree dir missing: %v", err)
	}
	if branch != "codebot/feat" {
		t.Errorf("branch = %q, want codebot/feat", branch)
	}

	if changed, err := HasChanges(dir); err != nil || changed {
		t.Errorf("fresh worktree: changed=%v err=%v, want clean", changed, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := HasChanges(dir); err != nil || !changed {
		t.Errorf("after edit: changed=%v err=%v, want dirty", changed, err)
	}

	infos, err := List(repo)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 1 || infos[0].Path != dir {
		t.Fatalf("List = %+v, want one entry at %s", infos, dir)
	}

	if _, err := Remove(repo, dir, branch, true); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("worktree dir should be gone after Remove")
	}
	if infos, _ := List(repo); len(infos) != 0 {
		t.Errorf("List = %d entries after Remove, want 0", len(infos))
	}
}

func TestCreateDuplicate(t *testing.T) {
	repo := initRepo(t)
	if _, _, err := Create(repo, "dup"); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, _, err := Create(repo, "dup"); err == nil {
		t.Error("duplicate Create should fail")
	}
}

func TestCopyIncludes(t *testing.T) {
	repo := initRepo(t)
	// .env is gitignored, so a clean worktree checkout omits it.
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("SECRET=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir, _, err := Create(repo, "feat")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Fatal("worktree should not have .env before CopyIncludes")
	}

	if failed, err := CopyIncludes(repo, dir, []string{".env"}); err != nil || len(failed) != 0 {
		t.Fatalf("CopyIncludes: failed=%v err=%v", failed, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatalf(".env not copied: %v", err)
	}
	if string(got) != "SECRET=1\n" {
		t.Errorf(".env content = %q, want SECRET=1", got)
	}
}

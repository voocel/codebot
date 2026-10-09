package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
}

// A worktree that is clean because the agent committed into it must not lose
// those commits on cleanup.
func TestRemoveKeepsCommittedWork(t *testing.T) {
	repo := initRepo(t)
	dir, branch, err := Create(repo, repo, "feat")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

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
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("checkout dir should be removed")
	}
	cmd := exec.Command("git", "-C", repo, "rev-parse", "--verify", branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("branch %s should still exist after Remove, got: %s", branch, out)
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

	dir, branch, err := Create(repo, repo, "feat")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("worktree dir missing: %v", err)
	}
	if branch != "codebot/feat" {
		t.Errorf("branch = %q, want codebot/feat", branch)
	}
	if _, _, err := Create(repo, repo, "feat"); err == nil {
		t.Error("duplicate Create should fail")
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

func TestCopyIncludes(t *testing.T) {
	repo := initRepo(t)
	// .env is gitignored, so a clean worktree checkout omits it.
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("SECRET=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir, _, err := Create(repo, repo, "feat")
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

// Another checkout's place of a directory counts; the rest of the repository,
// its git directory and other repositories do not.
func TestSamePlace(t *testing.T) {
	repo := initRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "pkg", "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "pkg", "a", "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-m", "pkg")
	wt := filepath.Join(t.TempDir(), "wt")
	gitIn(t, repo, "worktree", "add", "-b", "wt", wt)

	project := filepath.Join(repo, "pkg", "a")
	for dir, want := range map[string]bool{
		filepath.Join(wt, "pkg", "a"): true,
		wt:                            false,
		repo:                          false,
		filepath.Join(repo, ".git"):   false,
		initRepo(t):                   false,
	} {
		if got := SamePlace(project, dir); got != want {
			t.Errorf("SamePlace(project, %s) = %v, want %v", dir, got, want)
		}
	}
	if !SamePlace(repo, wt) {
		t.Error("a repository's root and its linked worktree's are not the same place")
	}
}

// A sandbox starts from the checkout it is made from.
func TestCreateFromAnotherCheckout(t *testing.T) {
	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	gitIn(t, repo, "worktree", "add", "-b", "wt", wt)
	if err := os.WriteFile(filepath.Join(wt, "feature.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "add", ".")
	gitIn(t, wt, "commit", "-m", "feature")

	dir, _, err := Create(repo, wt, "box")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "feature.txt")); err != nil {
		t.Errorf("the sandbox lacks the checkout's commit: %v", err)
	}
	root, _ := filepath.EvalSymlinks(Root(repo)) // dir has its links resolved
	if rel, err := filepath.Rel(root, dir); err != nil || strings.HasPrefix(rel, "..") {
		t.Errorf("the sandbox %s is not with the project's", dir)
	}
}

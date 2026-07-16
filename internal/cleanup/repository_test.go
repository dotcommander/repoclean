package cleanup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestWalkRepositoryParentDiscoveryIgnoreAndNestedRepository(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, ".gitignore", "*.log\n!keep.log\nsub/*.tmp\n")
	writeTestFile(t, root, "tracked.txt", "tracked")
	writeTestFile(t, root, "ignored.log", "ignored")
	writeTestFile(t, root, "keep.log", "kept")
	writeTestFile(t, root, ".git/info/exclude", "excluded.info\n")
	writeTestFile(t, root, "excluded.info", "excluded")
	writeTestFile(t, root, "sub/drop.tmp", "ignored")
	writeTestFile(t, root, "sub/visible.txt", "visible")
	writeTestFile(t, root, "sub/nested/.git/HEAD", "ref: refs/heads/main\n")
	writeTestFile(t, root, "sub/nested/private.txt", "private")
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("tracked.txt"); err != nil {
		t.Fatal(err)
	}

	scanRoot := filepath.Join(root, "sub")
	view, err := OpenRepository(scanRoot)
	if err != nil {
		t.Fatal(err)
	}
	if view == nil || view.Root != root || view.Prefix != "sub" {
		t.Fatalf("unexpected repository view: %#v", view)
	}
	files, err := WalkRepository(Config{Path: scanRoot, MaxDepth: 10}, view)
	if err != nil {
		t.Fatal(err)
	}
	byPath := filesByRelativePath(files)
	if !byPath["drop.tmp"].Ignored {
		t.Error("nested .gitignore pattern did not mark drop.tmp ignored")
	}
	if byPath["visible.txt"].Ignored {
		t.Error("visible.txt unexpectedly ignored")
	}
	if _, found := byPath[filepath.Join("nested", "private.txt")]; found {
		t.Error("nested repository content was included")
	}

	rootFiles, err := WalkRepository(Config{Path: root, MaxDepth: 10}, view)
	if err != nil {
		t.Fatal(err)
	}
	rootByPath := filesByRelativePath(rootFiles)
	if !rootByPath["tracked.txt"].Tracked {
		t.Error("index entry was not marked tracked")
	}
	if !rootByPath["ignored.log"].Ignored {
		t.Error("root ignore pattern did not mark ignored.log")
	}
	if rootByPath["keep.log"].Ignored {
		t.Error("negated ignore pattern did not restore keep.log")
	}
	if !rootByPath["excluded.info"].Ignored {
		t.Error("repository exclude did not mark excluded.info ignored")
	}
}

func TestWalkRepositoryUsesConfiguredExcludeFile(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "configured-ignore", "*.scratch\n")
	writeTestFile(t, root, "private.scratch", "ignored")

	cfg, err := repo.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Raw.SetOption("core", "", "excludesfile", "configured-ignore")
	if err := repo.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	view, err := OpenRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	files, err := WalkRepository(Config{Path: root, MaxDepth: 2}, view)
	if err != nil {
		t.Fatal(err)
	}
	if !filesByRelativePath(files)["private.scratch"].Ignored {
		t.Error("core.excludesFile pattern did not mark private.scratch ignored")
	}
}

func TestWalkRepositoryUsesGlobalExcludeFileWithUnsupportedConfigSyntax(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	excludeFile := filepath.Join(home, ".gitignore")
	writeTestFile(t, home, ".gitignore", "*.scratch\n")
	writeTestFile(t, home, ".gitconfig", "[core]\n\texcludesFile = \""+excludeFile+"\"\n[unsupported]\n\t.invalid = value\n")

	if _, err := git.PlainInit(root, false); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "private.scratch", "ignored")

	view, err := OpenRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	files, err := WalkRepository(Config{Path: root, MaxDepth: 2}, view)
	if err != nil {
		t.Fatal(err)
	}
	if !filesByRelativePath(files)["private.scratch"].Ignored {
		t.Error("global core.excludesFile pattern did not mark private.scratch ignored")
	}
}

func TestWalkRepositoryDistinguishesNoRepositoryFromUnknownState(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "loose.txt", "loose")
	files, err := WalkRepository(Config{Path: root, MaxDepth: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if files[0].GitStateUnknown {
		t.Error("confirmed filesystem-only scan marked repository state unknown")
	}
	MarkRepositoryStateUnknown(files)
	if !files[0].GitStateUnknown {
		t.Error("failed repository state was not marked unknown")
	}
}

func TestRepositoryHistoryUsesAllRefsAndBoundsDeletions(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-400 * 24 * time.Hour).Truncate(time.Second)
	commitFile(t, wt, root, "old.txt", "old", base)
	commitFile(t, wt, root, "delete-me.txt", "gone soon", base.Add(time.Hour))
	if err := os.Remove(filepath.Join(root, "delete-me.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Remove("delete-me.txt"); err != nil {
		t.Fatal(err)
	}
	commitTestChange(t, wt, "delete", base.Add(2*time.Hour))
	for i := 0; i < 200; i++ {
		commitFile(t, wt, root, "rolling.txt", string(rune('a'+i%26)), base.Add(time.Duration(i+3)*time.Hour))
	}
	view, err := NewRepositoryView(repo, root, root)
	if err != nil {
		t.Fatal(err)
	}
	stale, deleted, err := view.history(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if stale["old.txt"] < 300 {
		t.Fatalf("root-commit path staleness = %d, want >= 300", stale["old.txt"])
	}
	if deleted["delete-me.txt"] {
		t.Error("deletion older than the 200 newest commits was retained")
	}

	commitFile(t, wt, root, "recent-delete.txt", "gone", time.Now().Add(-2*time.Hour))
	if err := os.Remove(filepath.Join(root, "recent-delete.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Remove("recent-delete.txt"); err != nil {
		t.Fatal(err)
	}
	commitTestChange(t, wt, "recent delete", time.Now().Add(-time.Hour))
	_, deleted, err = view.history(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !deleted["recent-delete.txt"] {
		t.Error("recent deletion was not detected")
	}
}

func TestRepositoryHistoryTraversesDivergentRefs(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-10 * 24 * time.Hour)
	baseHash := commitFileHash(t, wt, root, "base.txt", "base", base)
	sideHash := commitFileHash(t, wt, root, "side-only.txt", "side", base.Add(time.Hour))
	if err := repo.Storer.SetReference(plumbing.NewHashReference("refs/heads/side", sideHash)); err != nil {
		t.Fatal(err)
	}
	if err := wt.Reset(&git.ResetOptions{Commit: baseHash, Mode: git.HardReset}); err != nil {
		t.Fatal(err)
	}
	commitFile(t, wt, root, "main-only.txt", "main", base.Add(2*time.Hour))
	view, err := NewRepositoryView(repo, root, root)
	if err != nil {
		t.Fatal(err)
	}
	stale, _, err := view.history(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, found := stale["side-only.txt"]; !found {
		t.Error("path reachable only from side ref was not traversed")
	}
	if _, found := stale["main-only.txt"]; !found {
		t.Error("path reachable from HEAD was not traversed")
	}
}

func writeTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitFile(t *testing.T, wt *git.Worktree, root, name, content string, when time.Time) {
	t.Helper()
	commitFileHash(t, wt, root, name, content, when)
}

func commitFileHash(t *testing.T, wt *git.Worktree, root, name, content string, when time.Time) plumbing.Hash {
	t.Helper()
	writeTestFile(t, root, name, content)
	if _, err := wt.Add(name); err != nil {
		t.Fatal(err)
	}
	return commitTestChange(t, wt, "update "+name, when)
}

func commitTestChange(t *testing.T, wt *git.Worktree, message string, when time.Time) plumbing.Hash {
	t.Helper()
	sig := &object.Signature{Name: "Test", Email: "test@example.com", When: when}
	hash, err := wt.Commit(message, &git.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func filesByRelativePath(files []FileInfo) map[string]FileInfo {
	result := make(map[string]FileInfo, len(files))
	for _, file := range files {
		result[file.RelPath] = file
	}
	return result
}

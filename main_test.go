package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
)

func TestScanDoesNotRequireGitExecutable(t *testing.T) {
	bin := buildRepocleanBinary(t)

	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("init repository: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("tracked"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("tracked.txt"); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "--path", root)
	cmd.Env = append(os.Environ(), "PATH="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("scan without git executable: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), `"path":`) {
		t.Fatalf("scan output is not JSON: %s", out)
	}
}

func buildRepocleanBinary(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "repoclean")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	modRoot := findModRoot(t)
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = modRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return bin
}

func findModRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file location")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func TestIntegrationCleanupScanner(t *testing.T) {
	bin := buildRepocleanBinary(t)

	// Set up a temp dir with a git repo and test files.
	tmpDir := t.TempDir()

	gitInit := exec.Command("git", "init", tmpDir)
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	// Tracked .go file: add and commit it.
	goFile := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAdd := exec.Command("git", "-C", tmpDir, "add", "main.go")
	if out, err := gitAdd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	gitCommit := exec.Command("git", "-C", tmpDir, "commit", "--allow-empty-message", "-m", "init", "--author=Test <t@t.com>")
	if out, err := gitCommit.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	// .DS_Store → delete candidate.
	if err := os.WriteFile(filepath.Join(tmpDir, ".DS_Store"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// .tmp file → delete candidate.
	if err := os.WriteFile(filepath.Join(tmpDir, "scratch.tmp"), []byte("temp"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Untracked .md → archive candidate.
	if err := os.WriteFile(filepath.Join(tmpDir, "notes.md"), []byte("some notes here"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Tracked non-allowlisted .md at root → misplaced doc.
	cookbookFile := filepath.Join(tmpDir, "cookbook.md")
	if err := os.WriteFile(cookbookFile, []byte("# Cookbook\nRecipes for things.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAddCookbook := exec.Command("git", "-C", tmpDir, "add", "cookbook.md")
	if out, err := gitAddCookbook.CombinedOutput(); err != nil {
		t.Fatalf("git add cookbook.md: %v\n%s", err, out)
	}
	gitCommitCookbook := exec.Command("git", "-C", tmpDir, "commit", "-m", "add cookbook", "--author=Test <t@t.com>")
	if out, err := gitCommitCookbook.CombinedOutput(); err != nil {
		t.Fatalf("git commit cookbook.md: %v\n%s", err, out)
	}

	// Tracked .env file → untrack candidate.
	envFile := filepath.Join(tmpDir, ".env")
	if err := os.WriteFile(envFile, []byte("SECRET=hunter2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAddEnv := exec.Command("git", "-C", tmpDir, "add", ".env")
	if out, err := gitAddEnv.CombinedOutput(); err != nil {
		t.Fatalf("git add .env: %v\n%s", err, out)
	}
	// Tracked binary file → untrack candidate.
	binFile := filepath.Join(tmpDir, "app.exe")
	if err := os.WriteFile(binFile, []byte("MZ\x00\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAddBin := exec.Command("git", "-C", tmpDir, "add", "-f", "app.exe")
	if out, err := gitAddBin.CombinedOutput(); err != nil {
		t.Fatalf("git add app.exe: %v\n%s", err, out)
	}
	gitCommitUntrack := exec.Command("git", "-C", tmpDir, "commit", "-m", "add env and binary", "--author=Test <t@t.com>")
	if out, err := gitCommitUntrack.CombinedOutput(); err != nil {
		t.Fatalf("git commit untrack files: %v\n%s", err, out)
	}

	// docs/ with uppercase filenames → rename_docs candidates.
	docsDir := filepath.Join(tmpDir, "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ARCHITECTURE.md", "DEMO_SCRIPT.md", "already-good.md"} {
		if err := os.WriteFile(filepath.Join(docsDir, name), []byte("# Doc\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitAddDocs := exec.Command("git", "-C", tmpDir, "add", "docs/")
	if out, err := gitAddDocs.CombinedOutput(); err != nil {
		t.Fatalf("git add docs/: %v\n%s", err, out)
	}
	gitCommitDocs := exec.Command("git", "-C", tmpDir, "commit", "-m", "add docs", "--author=Test <t@t.com>")
	if out, err := gitCommitDocs.CombinedOutput(); err != nil {
		t.Fatalf("git commit docs: %v\n%s", err, out)
	}

	// migrations/ with .sql file → should be clean, not archived.
	migDir := filepath.Join(tmpDir, "internal", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "001_init.sql"), []byte("CREATE TABLE t(id INT);"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAddMig := exec.Command("git", "-C", tmpDir, "add", "internal/")
	if out, err := gitAddMig.CombinedOutput(); err != nil {
		t.Fatalf("git add internal/: %v\n%s", err, out)
	}
	gitCommitMig := exec.Command("git", "-C", tmpDir, "commit", "-m", "add migrations", "--author=Test <t@t.com>")
	if out, err := gitCommitMig.CombinedOutput(); err != nil {
		t.Fatalf("git commit migrations: %v\n%s", err, out)
	}

	cmd := exec.Command(bin, "--path", tmpDir)
	rawOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("scanner failed: %v\noutput: %s", err, rawOut)
	}

	var result map[string]any
	if err := json.Unmarshal(rawOut, &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, rawOut)
	}

	summary, ok := result["summary"].(map[string]any)
	if !ok {
		t.Fatal("missing or wrong type for summary")
	}
	if summary["total"].(float64) == 0 {
		t.Error("summary.total should be > 0")
	}

	inList := func(key, name string) bool {
		arr, _ := result[key].([]any)
		for _, item := range arr {
			m, _ := item.(map[string]any)
			if f, _ := m["file"].(string); f == name {
				return true
			}
		}
		return false
	}

	if !inList("delete_candidates", ".DS_Store") {
		t.Error(".DS_Store should be in delete_candidates")
	}
	if !inList("delete_candidates", "scratch.tmp") {
		t.Error("scratch.tmp should be in delete_candidates")
	}
	if !inList("archive_candidates", "notes.md") {
		t.Error("notes.md should be in archive_candidates")
	}
	if !inList("misplaced_docs", "cookbook.md") {
		t.Error("cookbook.md should be in misplaced_docs")
	}
	if inList("dev_artifact_candidates", "cookbook.md") {
		t.Error("cookbook.md should NOT be in dev_artifact_candidates")
	}
	if inList("archive_candidates", "cookbook.md") {
		t.Error("cookbook.md should NOT be in archive_candidates")
	}
	if !inList("untrack_candidates", ".env") {
		t.Error(".env should be in untrack_candidates")
	}
	if !inList("untrack_candidates", "app.exe") {
		t.Error("app.exe should be in untrack_candidates")
	}

	// Rename docs checks.
	if !inList("rename_docs", "docs/ARCHITECTURE.md") {
		t.Error("docs/ARCHITECTURE.md should be in rename_docs")
	}
	if !inList("rename_docs", "docs/DEMO_SCRIPT.md") {
		t.Error("docs/DEMO_SCRIPT.md should be in rename_docs")
	}
	if inList("rename_docs", "docs/already-good.md") {
		t.Error("docs/already-good.md should NOT be in rename_docs")
	}

	// Rename target field check.
	renameTarget := func(key, name string) string {
		arr, _ := result[key].([]any)
		for _, item := range arr {
			m, _ := item.(map[string]any)
			if f, _ := m["file"].(string); f == name {
				s, _ := m["target"].(string)
				return s
			}
		}
		return ""
	}
	if got := renameTarget("rename_docs", "docs/ARCHITECTURE.md"); got != "docs/architecture.md" {
		t.Errorf("ARCHITECTURE.md target = %q, want docs/architecture.md", got)
	}
	if got := renameTarget("rename_docs", "docs/DEMO_SCRIPT.md"); got != "docs/demo-script.md" {
		t.Errorf("DEMO_SCRIPT.md target = %q, want docs/demo-script.md", got)
	}

	// Migration file should NOT be in archive_candidates.
	if inList("archive_candidates", "internal/migrations/001_init.sql") {
		t.Error("internal/migrations/001_init.sql should NOT be in archive_candidates")
	}
}

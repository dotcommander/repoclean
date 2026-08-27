package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dotcommander/repoclean/internal/cleanup"
)

func boolp(v bool) *bool { return &v }

func TestBuildCommandsCreatesScriptsDirForReferencedRootScripts(t *testing.T) {
	t.Parallel()

	cmds := buildCommands(cleanup.ScanResult{
		MisplacedScripts: []cleanup.FileCandidate{{
			File:       "deploy.sh",
			Referenced: boolp(true),
		}},
	})

	if len(cmds) != 2 {
		t.Fatalf("len(cmds) = %d, want 2: %#v", len(cmds), cmds)
	}
	if got, want := strings.Join(cmds[0].Args, " "), "mkdir -p scripts"; got != want {
		t.Fatalf("first command = %q, want %q", got, want)
	}
	if got, want := strings.Join(cmds[1].Args, " "), "git mv -- deploy.sh scripts/deploy.sh"; got != want {
		t.Fatalf("move command = %q, want %q", got, want)
	}
}

func TestBuildCommandsPreservesArchiveRelativePaths(t *testing.T) {
	t.Parallel()

	cmds := buildCommands(cleanup.ScanResult{
		ArchiveCandidates: []cleanup.FileCandidate{
			{File: "tmp/report.txt"},
			{File: "logs/report.txt"},
		},
	})

	var moves []string
	for _, cmd := range cmds {
		if len(cmd.Args) > 0 && cmd.Args[0] == "mv" {
			moves = append(moves, strings.Join(cmd.Args, " "))
		}
	}
	if len(moves) != 2 {
		t.Fatalf("archive moves = %v, want 2 moves", moves)
	}
	if !strings.Contains(moves[0], ".work/archive/") || !strings.HasSuffix(moves[0], "/tmp/report.txt") {
		t.Fatalf("first move did not preserve relative path: %q", moves[0])
	}
	if !strings.Contains(moves[1], ".work/archive/") || !strings.HasSuffix(moves[1], "/logs/report.txt") {
		t.Fatalf("second move did not preserve relative path: %q", moves[1])
	}
	if moves[0] == moves[1] {
		t.Fatalf("archive moves collide: %v", moves)
	}
}

func TestBuildCommandsUsesOptionTerminatorsForPathOperands(t *testing.T) {
	t.Parallel()

	cmds := buildCommands(cleanup.ScanResult{
		DeleteCandidates:  []cleanup.FileCandidate{{File: "-scratch.tmp"}},
		ArchiveCandidates: []cleanup.FileCandidate{{File: "-notes.md"}},
		UntrackCandidates: []cleanup.FileCandidate{{File: "-binary"}},
		RenameDocs:        []cleanup.FileCandidate{{File: "docs/OLD_NAME.md", Target: "docs/old-name.md"}},
	})

	joined := make([]string, 0, len(cmds))
	for _, cmd := range cmds {
		joined = append(joined, strings.Join(cmd.Args, " "))
	}
	want := []string{
		"rm -f -- -scratch.tmp",
		"mv -- -notes.md",
		"git rm --cached -- -binary",
		"git mv -- docs/OLD_NAME.md docs/old-name.md",
	}
	for _, needle := range want {
		found := false
		for _, got := range joined {
			if strings.Contains(got, needle) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing command containing %q in %v", needle, joined)
		}
	}
}

func TestRunCommandsReturnsErrorOnCommandFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	err := runCommands([]cleanupCmd{{
		Category: "test",
		Args:     []string{"mv", "--", "missing.txt", "dest.txt"},
	}}, dir)
	if err == nil {
		t.Fatal("runCommands returned nil for a failed command")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "dest.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("dest.txt exists or stat failed unexpectedly: %v", statErr)
	}
}

func TestRunCommandsStopsAfterFirstFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sentinel := filepath.Join(dir, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	err := runCommandsTo(&output, []cleanupCmd{
		{Category: "test", Args: []string{"mv", "--", "missing.txt", "dest.txt"}, Kind: actionMove, Source: "missing.txt", Target: "dest.txt"},
		{Category: "test", Args: []string{"rm", "-f", "--", "sentinel.txt"}, Kind: actionRemove, Target: "sentinel.txt"},
	}, dir)
	if err == nil {
		t.Fatal("runCommands returned nil for a failed command")
	}
	contents, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("read sentinel after failure: %v", err)
	}
	if string(contents) != "keep" {
		t.Fatalf("sentinel contents = %q, want keep", contents)
	}
	if !strings.Contains(output.String(), "backup: .work/archive/pre-cleanup-") || !strings.Contains(output.String(), "(1 files)") {
		t.Fatalf("backup output = %q, want one archived file", output.String())
	}
	if !strings.Contains(output.String(), "done: 0 executed, 0 skipped, 1 failed, 1 not run") {
		t.Fatalf("summary output = %q, want one failed and one not run", output.String())
	}
}

func TestRunCommandsBacksUpExistingMoveDestination(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	source := "source.txt"
	target := filepath.Join("archive", "source.txt")
	if err := os.WriteFile(filepath.Join(dir, source), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, target), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	targets := collectTargets([]cleanupCmd{{Args: []string{"mv", "--", source, target}, Kind: actionMove, Source: source, Target: target}})
	if got := strings.Join(targets, ","); got != source+","+target {
		t.Fatalf("move backup targets = %q, want source and target", got)
	}

	var output bytes.Buffer
	runErr := runCommandsTo(&output, []cleanupCmd{{
		Category: "archive",
		Args:     []string{"mv", "--", source, target},
		Kind:     actionMove,
		Source:   source,
		Target:   target,
	}}, dir)

	matches, err := filepath.Glob(filepath.Join(dir, ".work", "archive", "pre-cleanup-*.tar.gz"))
	if err != nil {
		t.Fatalf("glob backup archive: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("backup archive count = %d, want 1: %v", len(matches), matches)
	}
	entries := readBackupEntries(t, matches[0])
	if got := entries[source]; got != "new" {
		t.Fatalf("source backup = %q, want new", got)
	}
	if got := entries[filepath.ToSlash(target)]; got != "existing" {
		t.Fatalf("target backup = %q, want existing", got)
	}
	if !strings.Contains(output.String(), "(2 files)") {
		t.Fatalf("backup output = %q, want two archived files", output.String())
	}
	if runErr == nil {
		if _, err := os.Stat(filepath.Join(dir, source)); !os.IsNotExist(err) {
			t.Fatalf("source still exists after successful move: %v", err)
		}
		contents, err := os.ReadFile(filepath.Join(dir, target))
		if err != nil {
			t.Fatalf("read moved target: %v", err)
		}
		if string(contents) != "new" {
			t.Fatalf("moved target = %q, want new", contents)
		}
	} else {
		for path, want := range map[string]string{source: "new", target: "existing"} {
			contents, err := os.ReadFile(filepath.Join(dir, path))
			if err != nil {
				t.Fatalf("read %s after rejected move: %v", path, err)
			}
			if string(contents) != want {
				t.Fatalf("%s after rejected move = %q, want %q", path, contents, want)
			}
		}
	}
}

func readBackupEntries(t *testing.T, path string) map[string]string {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gz.Close() })
	tr := tar.NewReader(gz)
	entries := map[string]string{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return entries
		}
		if err != nil {
			t.Fatal(err)
		}
		if !header.FileInfo().Mode().IsRegular() {
			continue
		}
		contents, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		entries[header.Name] = string(contents)
	}
}

func TestRunCommandsBacksUpLeadingDashPaths(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "-scratch.tmp"), []byte("scratch"), 0o644); err != nil {
		t.Fatalf("write leading-dash file: %v", err)
	}

	err := runCommands([]cleanupCmd{{
		Category: "test",
		Args:     []string{"rm", "-f", "--", "-scratch.tmp"},
	}}, dir)
	if err != nil {
		t.Fatalf("runCommands returned error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "-scratch.tmp")); !os.IsNotExist(statErr) {
		t.Fatalf("-scratch.tmp still exists or stat failed unexpectedly: %v", statErr)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".work", "archive", "pre-cleanup-*.tar.gz"))
	if err != nil {
		t.Fatalf("glob backup archive: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("backup archive count = %d, want 1: %v", len(matches), matches)
	}
	f, err := os.Open(matches[0])
	if err != nil {
		t.Fatalf("open backup archive: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("open backup gzip: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	header, err := tr.Next()
	if err != nil {
		t.Fatalf("read backup header: %v", err)
	}
	if header.Name != "-scratch.tmp" {
		t.Fatalf("backup entry = %q, want -scratch.tmp", header.Name)
	}
	contents, err := io.ReadAll(tr)
	if err != nil {
		t.Fatalf("read backup contents: %v", err)
	}
	if string(contents) != "scratch" {
		t.Fatalf("backup contents = %q, want scratch", contents)
	}
}

func TestExecuteFilesystemActionsWithoutPATHTools(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	actions := []cleanupCmd{
		{Kind: actionMkdir, Target: "archive"},
		{Kind: actionMove, Source: "source", Target: "archive/moved"},
		{Kind: actionRemove, Target: "archive/moved"},
	}
	for _, action := range actions {
		if _, err := executeAction(action, dir); err != nil {
			t.Fatalf("execute filesystem action: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "archive", "moved")); !os.IsNotExist(err) {
		t.Fatalf("moved file still exists or stat failed unexpectedly: %v", err)
	}
}

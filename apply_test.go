package main

import (
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

package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dotcommander/repoclean/internal/cleanup"
)

const defaultCommandTimeout = 1 * time.Minute

func shellQuote(path string) string {
	if strings.ContainsAny(path, " \t'\"\\$`!#&|;(){}[]<>?*~") {
		return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
	}
	return path
}

type cleanupCmd struct {
	Category string   // section label
	Args     []string // command + args (no shell interpolation)
	Comment  bool     // true = informational, don't execute
	Kind     actionKind
	Source   string
	Target   string
}

type actionKind int

const (
	actionCommand actionKind = iota
	actionMkdir
	actionRemove
	actionMove
)

func archiveDest(archiveDir, relPath string) string {
	clean := filepath.Clean(relPath)
	if clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
		return filepath.Join(archiveDir, filepath.Base(relPath))
	}
	return filepath.Join(archiveDir, clean)
}

func appendMkdir(cmds []cleanupCmd, category, dir string, seen map[string]bool) []cleanupCmd {
	if dir == "." || dir == "" || seen[dir] {
		return cmds
	}
	seen[dir] = true
	return append(cmds, cleanupCmd{Category: category, Args: []string{"mkdir", "-p", dir}, Kind: actionMkdir, Target: dir})
}

func buildCommands(result cleanup.ScanResult) []cleanupCmd {
	var cmds []cleanupCmd
	date := time.Now().Format("2006-01-02")
	archiveDir := ".work/archive/" + date
	mkdirs := map[string]bool{}

	needsArchive := len(result.ArchiveCandidates) > 0 || len(result.DevArtifactCandidates) > 0
	for _, c := range result.MisplacedScripts {
		if c.Referenced != nil && !*c.Referenced {
			needsArchive = true
			break
		}
	}
	if needsArchive {
		cmds = appendMkdir(cmds, "setup", archiveDir, mkdirs)
	}

	for _, c := range result.DeleteCandidates {
		if strings.HasSuffix(c.Reason, "directory") {
			cmds = append(cmds, cleanupCmd{Category: "delete", Args: []string{"rm", "-rf", "--", c.File}, Kind: actionRemove, Target: c.File})
		} else {
			cmds = append(cmds, cleanupCmd{Category: "delete", Args: []string{"rm", "-f", "--", c.File}, Kind: actionRemove, Target: c.File})
		}
	}

	for _, c := range result.DevArtifactCandidates {
		dest := archiveDest(archiveDir, c.File)
		cmds = appendMkdir(cmds, "dev_artifact", filepath.Dir(dest), mkdirs)
		cmds = append(cmds,
			cleanupCmd{Category: "dev_artifact", Args: []string{"git", "rm", "--cached", "--", c.File}},
			cleanupCmd{Category: "dev_artifact", Args: []string{"mv", "--", c.File, dest}, Kind: actionMove, Source: c.File, Target: dest},
		)
	}

	for _, c := range result.ArchiveCandidates {
		dest := archiveDest(archiveDir, c.File)
		cmds = appendMkdir(cmds, "archive", filepath.Dir(dest), mkdirs)
		cmds = append(cmds, cleanupCmd{Category: "archive", Args: []string{"mv", "--", c.File, dest}, Kind: actionMove, Source: c.File, Target: dest})
	}

	for _, c := range result.MisplacedDocs {
		cmds = append(cmds,
			cleanupCmd{Category: "misplaced_docs", Args: []string{"mkdir", "-p", "docs"}, Kind: actionMkdir, Target: "docs"},
			cleanupCmd{Category: "misplaced_docs", Args: []string{"git", "mv", "--", c.File, "docs/" + c.File}},
		)
	}

	for _, c := range result.MisplacedScripts {
		if c.Referenced != nil && *c.Referenced {
			cmds = appendMkdir(cmds, "misplaced_scripts", "scripts", mkdirs)
			cmds = append(cmds, cleanupCmd{Category: "misplaced_scripts", Args: []string{"git", "mv", "--", c.File, "scripts/" + c.File}})
		} else {
			dest := archiveDest(archiveDir, c.File)
			cmds = appendMkdir(cmds, "misplaced_scripts", filepath.Dir(dest), mkdirs)
			cmds = append(cmds, cleanupCmd{Category: "misplaced_scripts", Args: []string{"mv", "--", c.File, dest}, Kind: actionMove, Source: c.File, Target: dest})
		}
	}

	for _, c := range result.RenameDocs {
		cmds = append(cmds, cleanupCmd{Category: "rename_docs", Args: []string{"git", "mv", "--", c.File, c.Target}})
	}

	for _, c := range result.UntrackCandidates {
		cmds = append(cmds, cleanupCmd{Category: "untrack", Args: []string{"git", "rm", "--cached", "--", c.File}})
	}

	for _, c := range result.LargeFiles {
		cmds = append(cmds, cleanupCmd{
			Category: "review",
			Args:     []string{"echo", fmt.Sprintf("large: %s (%s)", c.File, fmtSize(c.SizeKB))},
			Comment:  true,
		})
	}
	for _, c := range result.BrokenLinks {
		cmds = append(cmds, cleanupCmd{
			Category: "review",
			Args:     []string{"echo", fmt.Sprintf("broken: %s -> %s", c.File, c.Target)},
			Comment:  true,
		})
	}

	return cmds
}

func printExec(result cleanup.ScanResult) {
	cmds := buildCommands(result)
	lastCat := ""
	for _, c := range cmds {
		if c.Category != lastCat {
			if c.Comment {
				fmt.Printf("# REVIEW (manual — do not execute)\n")
			} else {
				fmt.Printf("# %s\n", c.Category)
			}
			lastCat = c.Category
		}
		if c.Comment {
			fmt.Printf("# %s\n", strings.Join(c.Args[1:], " "))
		} else {
			parts := make([]string, len(c.Args))
			for i, a := range c.Args {
				parts[i] = shellQuote(a)
			}
			fmt.Println(strings.Join(parts, " "))
		}
	}
}

func dryRun(cmds []cleanupCmd) {
	lastCat := ""
	actionable := 0
	for _, c := range cmds {
		if c.Category != lastCat {
			if lastCat != "" {
				fmt.Println()
			}
			fmt.Printf("# %s\n", c.Category)
			lastCat = c.Category
		}
		display := strings.Join(c.Args, " ")
		if c.Comment {
			fmt.Printf("  skip  %s\n", display)
		} else {
			fmt.Printf("  run   %s\n", display)
			actionable++
		}
	}
	fmt.Printf("\ndry-run: %d commands would execute. Re-run with --confirm to apply.\n", actionable)
}

// collectTargets returns all file paths that destructive commands will touch.
func collectTargets(cmds []cleanupCmd) []string {
	var paths []string
	seen := map[string]bool{}
	for _, c := range cmds {
		if c.Comment || len(c.Args) < 2 {
			continue
		}
		if c.Kind == actionRemove {
			if !seen[c.Target] {
				paths = append(paths, c.Target)
				seen[c.Target] = true
			}
			continue
		}
		if c.Kind == actionMove {
			if !seen[c.Source] {
				paths = append(paths, c.Source)
				seen[c.Source] = true
			}
			continue
		}
		switch c.Args[0] {
		case "rm":
			p := c.Args[len(c.Args)-1]
			if !seen[p] {
				paths = append(paths, p)
				seen[p] = true
			}
		case "mv":
			if len(c.Args) >= 4 {
				p := c.Args[len(c.Args)-2]
				if !seen[p] {
					paths = append(paths, p)
					seen[p] = true
				}
			}
		case "git":
			// git rm, git mv — the file being affected
			if len(c.Args) >= 3 && (c.Args[1] == "rm" || c.Args[1] == "mv") {
				var p string
				if c.Args[1] == "rm" {
					p = c.Args[len(c.Args)-1]
				} else {
					// git mv src dest — back up src
					p = c.Args[len(c.Args)-2]
				}
				if !seen[p] {
					paths = append(paths, p)
					seen[p] = true
				}
			}
		}
	}
	return paths
}

func createBackup(dir string, targets []string) (string, error) {
	ts := time.Now().Format("20060102-150405")
	backupDir := filepath.Join(dir, ".work", "archive")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	tarPath := filepath.Join(backupDir, "pre-cleanup-"+ts+".tar.gz")

	// Filter to files that actually exist.
	var existing []string
	for _, t := range targets {
		full := t
		if !filepath.IsAbs(t) {
			full = filepath.Join(dir, t)
		}
		if _, err := os.Lstat(full); err == nil {
			existing = append(existing, t)
		}
	}
	if len(existing) == 0 {
		return "", nil
	}

	out, err := os.Create(tarPath)
	if err != nil {
		return "", fmt.Errorf("create backup: %w", err)
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	for _, name := range existing {
		if err := addBackupPath(tw, dir, name); err != nil {
			tw.Close()
			gz.Close()
			out.Close()
			return "", err
		}
	}
	if err := tw.Close(); err != nil {
		return "", fmt.Errorf("finish backup tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return "", fmt.Errorf("finish backup gzip: %w", err)
	}
	if err := out.Close(); err != nil {
		return "", fmt.Errorf("close backup: %w", err)
	}
	return tarPath, nil
}

func addBackupPath(tw *tar.Writer, root, name string) error {
	full := name
	if !filepath.IsAbs(full) {
		full = filepath.Join(root, full)
	}
	return filepath.Walk(full, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("read backup path %s: %w", name, err)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("archive %s: %w", path, err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			return fmt.Errorf("backup path %q is outside scan root", name)
		}
		header.Name = filepath.ToSlash(rel)
		if info.Mode()&os.ModeSymlink != 0 {
			header.Linkname, err = os.Readlink(path)
			if err != nil {
				return fmt.Errorf("read backup symlink %s: %w", path, err)
			}
		}
		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("write backup header %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open backup file %s: %w", path, err)
		}
		_, copyErr := io.Copy(tw, f)
		closeErr := f.Close()
		if copyErr != nil {
			return fmt.Errorf("copy backup file %s: %w", path, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close backup file %s: %w", path, closeErr)
		}
		return nil
	})
}

func executeAction(c cleanupCmd, dir string) ([]byte, error) {
	resolve := func(path string) string {
		if filepath.IsAbs(path) {
			return path
		}
		return filepath.Join(dir, path)
	}
	switch c.Kind {
	case actionMkdir:
		return nil, os.MkdirAll(resolve(c.Target), 0o755)
	case actionRemove:
		return nil, os.RemoveAll(resolve(c.Target))
	case actionMove:
		return nil, os.Rename(resolve(c.Source), resolve(c.Target))
	default:
		ctx, cancel := context.WithTimeout(context.Background(), defaultCommandTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, c.Args[0], c.Args[1:]...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			return out, fmt.Errorf("timeout after %s: %w", defaultCommandTimeout, ctx.Err())
		}
		return out, err
	}
}

func runCommands(cmds []cleanupCmd, dir string) error {
	// Back up all files that will be touched.
	targets := collectTargets(cmds)
	if len(targets) > 0 {
		tarPath, err := createBackup(dir, targets)
		if err != nil {
			return fmt.Errorf("backup failed: %w", err)
		}
		if tarPath != "" {
			rel, _ := filepath.Rel(dir, tarPath)
			if rel == "" {
				rel = tarPath
			}
			fmt.Printf("backup: %s (%d files)\n\n", rel, len(targets))
		}
	}

	var executed, skipped, failed int
	lastCat := ""

	for _, c := range cmds {
		if c.Category != lastCat {
			if lastCat != "" {
				fmt.Println()
			}
			lastCat = c.Category
		}

		display := strings.Join(c.Args, " ")

		if c.Comment {
			fmt.Printf("  # %s\n", display)
			skipped++
			continue
		}

		fmt.Printf("  %s", display)
		out, err := executeAction(c, dir)
		if err != nil {
			fmt.Printf(" FAIL: %v\n", err)
			if len(out) > 0 {
				fmt.Printf("    %s\n", strings.TrimSpace(string(out)))
			}
			failed++
		} else {
			fmt.Println(" ok")
			executed++
		}
	}

	fmt.Printf("\ndone: %d executed, %d skipped, %d failed\n", executed, skipped, failed)
	if failed > 0 {
		return fmt.Errorf("%d command(s) failed", failed)
	}
	return nil
}

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dotcommander/repoclean/internal/cleanup"
)

func main() {
	path := flag.String("path", ".", "directory to scan")
	maxDepth := flag.Int("max-depth", 5, "maximum directory depth")
	staleDays := flag.Int("stale-days", 90, "days before a file is considered stale")
	report := flag.Bool("report", false, "output human-readable cleanup plan instead of JSON")
	execMode := flag.Bool("exec", false, "output shell commands for automated execution")
	apply := flag.Bool("apply", false, "dry-run: show commands that would execute (add --confirm to run)")
	confirm := flag.Bool("confirm", false, "actually execute commands (requires --apply)")
	flag.Parse()

	absPath, err := filepath.Abs(*path)
	if err != nil {
		log.Fatalf("cleanup-scanner: resolve path: %v", err)
	}

	cfg := cleanup.Config{
		Path:      absPath,
		MaxDepth:  *maxDepth,
		StaleDays: *staleDays,
	}

	start := time.Now()
	files, err := cleanup.Walk(cfg)
	if err != nil {
		log.Fatalf("cleanup-scanner: walk: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		log.Printf("cleanup-scanner: walk took %v (large repo?)", time.Since(start))
	}

	if err := cleanup.Enrich(files, cfg); err != nil {
		log.Printf("cleanup-scanner: enrich warning: %v", err)
	}

	if err := cleanup.Classify(files); err != nil {
		log.Printf("cleanup-scanner: classify warning: %v", err)
	}

	cleanup.FindDuplicates(files)
	cleanup.EmitFindings(files)

	result := categorize(files, cfg)
	result.Path = absPath

	if *confirm && !*apply {
		log.Fatal("cleanup-scanner: --confirm requires --apply")
	}

	if *apply {
		cmds := buildCommands(result)
		if len(cmds) == 0 {
			fmt.Println("nothing to clean up")
			return
		}
		if *confirm {
			runCommands(cmds, absPath)
		} else {
			dryRun(cmds)
		}
		return
	}

	if *execMode {
		printExec(result)
		return
	}

	if *report {
		printReport(result)
		return
	}

	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		log.Fatalf("cleanup-scanner: marshal: %v", err)
	}
	fmt.Println(string(out))
}

func fmtSize(kb int64) string {
	if kb >= 1024 {
		return fmt.Sprintf("%4dMB", kb/1024)
	}
	return fmt.Sprintf("%4dKB", kb)
}

func boolStr(b *bool) string {
	if b == nil {
		return "unknown"
	}
	if *b {
		return "yes"
	}
	return "no"
}

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
}

func buildCommands(result cleanup.ScanResult) []cleanupCmd {
	var cmds []cleanupCmd
	date := time.Now().Format("2006-01-02")
	archiveDir := ".work/archive/" + date

	needsArchive := len(result.ArchiveCandidates) > 0 || len(result.DevArtifactCandidates) > 0
	for _, c := range result.MisplacedScripts {
		if c.Referenced != nil && !*c.Referenced {
			needsArchive = true
			break
		}
	}
	if needsArchive {
		cmds = append(cmds, cleanupCmd{Category: "setup", Args: []string{"mkdir", "-p", archiveDir}})
	}

	for _, c := range result.DeleteCandidates {
		if strings.HasSuffix(c.Reason, "directory") {
			cmds = append(cmds, cleanupCmd{Category: "delete", Args: []string{"rm", "-rf", c.File}})
		} else {
			cmds = append(cmds, cleanupCmd{Category: "delete", Args: []string{"rm", "-f", c.File}})
		}
	}

	for _, c := range result.DevArtifactCandidates {
		dest := archiveDir + "/" + filepath.Base(c.File)
		cmds = append(cmds,
			cleanupCmd{Category: "dev_artifact", Args: []string{"git", "rm", "--cached", c.File}},
			cleanupCmd{Category: "dev_artifact", Args: []string{"mv", c.File, dest}},
		)
	}

	for _, c := range result.ArchiveCandidates {
		dest := archiveDir + "/" + filepath.Base(c.File)
		cmds = append(cmds, cleanupCmd{Category: "archive", Args: []string{"mv", c.File, dest}})
	}

	for _, c := range result.MisplacedDocs {
		cmds = append(cmds,
			cleanupCmd{Category: "misplaced_docs", Args: []string{"mkdir", "-p", "docs"}},
			cleanupCmd{Category: "misplaced_docs", Args: []string{"git", "mv", c.File, "docs/" + c.File}},
		)
	}

	for _, c := range result.MisplacedScripts {
		if c.Referenced != nil && *c.Referenced {
			cmds = append(cmds, cleanupCmd{Category: "misplaced_scripts", Args: []string{"git", "mv", c.File, "scripts/" + c.File}})
		} else {
			dest := archiveDir + "/" + filepath.Base(c.File)
			cmds = append(cmds, cleanupCmd{Category: "misplaced_scripts", Args: []string{"mv", c.File, dest}})
		}
	}

	for _, c := range result.RenameDocs {
		cmds = append(cmds, cleanupCmd{Category: "rename_docs", Args: []string{"git", "mv", c.File, c.Target}})
	}

	for _, c := range result.UntrackCandidates {
		cmds = append(cmds, cleanupCmd{Category: "untrack", Args: []string{"git", "rm", "--cached", c.File}})
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
		switch c.Args[0] {
		case "rm":
			// last arg is the path
			p := c.Args[len(c.Args)-1]
			if !seen[p] {
				paths = append(paths, p)
				seen[p] = true
			}
		case "mv":
			// source is second-to-last
			if len(c.Args) >= 3 {
				p := c.Args[1]
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
					p = c.Args[2]
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

	args := []string{"czf", tarPath, "-C", dir}
	args = append(args, existing...)
	cmd := exec.Command("tar", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("tar: %v\n%s", err, out)
	}
	return tarPath, nil
}

func runCommands(cmds []cleanupCmd, dir string) {
	// Back up all files that will be touched.
	targets := collectTargets(cmds)
	if len(targets) > 0 {
		tarPath, err := createBackup(dir, targets)
		if err != nil {
			log.Fatalf("cleanup-scanner: backup failed: %v", err)
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
		cmd := exec.Command(c.Args[0], c.Args[1:]...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
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
}

type reportSection struct {
	Title  string // fmt format with one %d for count
	Format string
	Header []string
	Rows   [][]string
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func printSections(w *os.File, sections []reportSection) {
	for _, s := range sections {
		if len(s.Rows) == 0 {
			continue
		}
		fmt.Fprintf(w, s.Title+"\n", len(s.Rows))
		fmt.Fprintf(w, s.Format, toAny(s.Header)...)
		for _, row := range s.Rows {
			fmt.Fprintf(w, s.Format, toAny(row)...)
		}
		fmt.Fprintln(w)
	}
}

func printReport(result cleanup.ScanResult) {
	w := os.Stdout
	bar := strings.Repeat("\u2550", 30)
	fmt.Fprintf(w, "Cleanup Plan \u2014 %s\n%s\n\n", result.Path, bar)

	sections := []reportSection{
		{Title: "DELETE (%d files)", Format: "  %-8s %-30s %-20s %s\n", Header: []string{"Action", "File", "Reason", "Size"}},
		{Title: "DEV ARTIFACTS (%d files \u2014 untrack + archive)", Format: "  %-8s %-30s %-20s %s\n", Header: []string{"Action", "File", "Reason", "Size"}},
		{Title: "ARCHIVE (%d files \u2014 move to .work/archive/)", Format: "  %-8s %-30s %-20s %6s  %s\n", Header: []string{"Action", "File", "Reason", "Score", "Hint"}},
		{Title: "LARGE FILES (%d file \u2014 manual review)", Format: "  %-8s %-30s %-10s %s\n", Header: []string{"Action", "File", "Size", "Tracked"}},
		{Title: "MISPLACED DOCS (%d files \u2014 move to docs/)", Format: "  %-8s %-30s %s\n", Header: []string{"Action", "File", "Reason"}},
		{Title: "RENAME DOCS (%d files \u2014 normalize filenames)", Format: "  %-8s %-40s %s\n", Header: []string{"Action", "File", "New Name"}},
		{Title: "MISPLACED SCRIPTS (%d file)", Format: "  %-8s %-30s %-10s %s\n", Header: []string{"Action", "File", "Tracked", "Referenced"}},
		{Title: "UNTRACK (%d files \u2014 should not be in git)", Format: "  %-8s %-30s %-25s %s\n", Header: []string{"Action", "File", "Reason", "Size"}},
		{Title: "BROKEN LINKS (%d)", Format: "  %-8s %-30s %s\n", Header: []string{"Action", "File", "Target"}},
	}

	for _, c := range result.DeleteCandidates {
		sections[0].Rows = append(sections[0].Rows, []string{"delete", c.File, c.Reason, fmtSize(c.SizeKB)})
	}
	for _, c := range result.DevArtifactCandidates {
		sections[1].Rows = append(sections[1].Rows, []string{"git rm", c.File, c.Reason, fmtSize(c.SizeKB)})
	}
	for _, c := range result.ArchiveCandidates {
		sections[2].Rows = append(sections[2].Rows, []string{"archive", c.File, c.Reason, fmt.Sprintf("%d", c.Score), c.ContentHint})
	}
	for _, c := range result.LargeFiles {
		sections[3].Rows = append(sections[3].Rows, []string{"review", c.File, fmtSize(c.SizeKB), boolStr(c.Tracked)})
	}
	for _, c := range result.MisplacedDocs {
		sections[4].Rows = append(sections[4].Rows, []string{"move \u2192", c.File + " \u2192 docs/" + c.File, c.Reason})
	}
	for _, c := range result.RenameDocs {
		sections[5].Rows = append(sections[5].Rows, []string{"rename", c.File, c.Target})
	}
	for _, c := range result.MisplacedScripts {
		sections[6].Rows = append(sections[6].Rows, []string{"move \u2192", c.File + " \u2192 scripts/", boolStr(c.Tracked), boolStr(c.Referenced)})
	}
	for _, c := range result.UntrackCandidates {
		sections[7].Rows = append(sections[7].Rows, []string{"untrack", c.File, c.Reason, fmtSize(c.SizeKB)})
	}
	for _, c := range result.BrokenLinks {
		sections[8].Rows = append(sections[8].Rows, []string{"unlink", c.File, c.Target})
	}

	printSections(w, sections)

	if len(result.AllFiles) > 0 {
		fmt.Fprintf(w, "ALL FILES (%d)\n", len(result.AllFiles))
		fmt.Fprintf(w, "  %-18s %-40s %s\n", "Status", "File", "Reason")
		for _, f := range result.AllFiles {
			reason := f.Reason
			if reason == "" {
				reason = "-"
			}
			fmt.Fprintf(w, "  %-18s %-40s %s\n", f.Status, f.File, reason)
		}
		fmt.Fprintln(w)
	}

	var parts []string
	if n := len(result.DeleteCandidates); n > 0 {
		parts = append(parts, fmt.Sprintf("%d delete", n))
	}
	if n := len(result.DevArtifactCandidates); n > 0 {
		parts = append(parts, fmt.Sprintf("%d untrack", n))
	}
	if n := len(result.ArchiveCandidates); n > 0 {
		parts = append(parts, fmt.Sprintf("%d archive", n))
	}
	if n := len(result.LargeFiles); n > 0 {
		parts = append(parts, fmt.Sprintf("%d review", n))
	}
	if n := len(result.MisplacedScripts); n > 0 {
		parts = append(parts, fmt.Sprintf("%d move scripts", n))
	}
	if n := len(result.MisplacedDocs); n > 0 {
		parts = append(parts, fmt.Sprintf("%d move docs", n))
	}
	if n := len(result.RenameDocs); n > 0 {
		parts = append(parts, fmt.Sprintf("%d rename docs", n))
	}
	if n := len(result.UntrackCandidates); n > 0 {
		parts = append(parts, fmt.Sprintf("%d untrack", n))
	}
	if n := len(result.BrokenLinks); n > 0 {
		parts = append(parts, fmt.Sprintf("%d broken links", n))
	}
	if len(parts) == 0 {
		fmt.Fprintln(w, "Summary: nothing to clean up")
	} else {
		fmt.Fprintf(w, "Summary: %s\n", strings.Join(parts, ", "))
	}
}

var (
	// Dotfiles/dotdirs we actually care about — everything else is skipped.
	dotfileWhitelist = map[string]bool{
		".DS_Store":      true,
		".env":           true,
		".pytest_cache":  true,
		".mypy_cache":    true,
	}
	deleteNames = map[string]bool{
		".DS_Store": true, "Thumbs.db": true, "desktop.ini": true,
	}
	deleteExts = map[string]bool{
		".tmp": true, ".log": true, ".swp": true,
	}
	// Archive file extensions — move to .work/ instead of deleting.
	archiveExts = map[string]bool{
		".zip": true, ".tar": true, ".gz": true, ".tgz": true,
		".rar": true, ".7z": true, ".bz2": true, ".xz": true,
	}
	// Extensions that are images — should not be tracked at repo root.
	imageExts = map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
		".svg": true, ".ico": true, ".webp": true, ".bmp": true,
	}
	// Patterns for ignored files that are dev junk and should be deleted.
	// Checked via strings.Contains or suffix matching.
	ignoredDevDocSuffixes = []string{
		"_GUIDE.md", "_REPORT.md", "_IMPLEMENTATION.md", "_ANALYSIS.md",
		"_PLAN.md", "_SUMMARY.md", "_PROGRESS.md", "_RESULTS.md",
	}
	ignoredDeletePrefixes = []string{"verify-"}
	// Directories where ignored files are expected runtime data — never touch.
	ignoredSafeDirs = []string{"data/", "bin/", "cache/", ".work/"}
	// Data files: NEVER delete. Route to archive with prominent warning.
	dataExts = map[string]bool{
		".db": true, ".sqlite": true, ".sqlite3": true,
		".bak": true, ".sql": true, ".csv": true,
	}
	deleteDirs = map[string]bool{
		"node_modules": true, "__pycache__": true,
		".pytest_cache": true, ".mypy_cache": true,
	}
	devArtifactPrefixes = []string{"looper", "flow"}
	devArtifactSuffixes = []string{"_SUMMARY.md", "-spec.md", "_spec.md", "-state.json"}
	// Allowlisted .md files at repo root — everything else is a dev artifact.
	allowedRootMD = map[string]bool{
		"README.md": true, "CLAUDE.md": true, "CHANGELOG.md": true,
		"LICENSE.md": true, "CONTRIBUTING.md": true, "SECURITY.md": true,
		"CODE_OF_CONDUCT.md": true, "RTK.md": true,
	}
	// Scaffold files: tracked files that are leftover from project init (e.g., Vite, CRA).
	scaffoldFiles = map[string]bool{
		"public/vite.svg": true, "public/favicon.ico": true,
		"src/logo.svg": true, "src/App.css": true,
	}
	// Extensions that typically shouldn't be tracked in git.
	untrackExts = map[string]bool{
		".exe": true, ".dll": true, ".so": true, ".dylib": true,
		".o": true, ".a": true, ".obj": true, ".lib": true,
		".zip": true, ".tar": true, ".gz": true, ".tgz": true,
		".rar": true, ".7z": true, ".wasm": true,
	}
	// Directory prefixes where tracked files are likely build output.
	untrackDirs = []string{"dist/", "build/", "out/", "target/"}
)

func isDeleteCandidate(f *cleanup.FileInfo) (bool, string) {
	name := filepath.Base(f.RelPath)

	if strings.HasPrefix(name, ".looper") {
		return true, "looper remnant"
	}

	if deleteNames[name] {
		return true, "system file"
	}

	ext := filepath.Ext(name)
	if deleteExts[ext] || strings.HasSuffix(name, "~") {
		return true, "temp/backup file"
	}

	if f.IsDir && deleteDirs[name] {
		return true, "cache directory"
	}

	if f.IsEmpty {
		return true, "empty directory"
	}

	return false, ""
}

func isDevArtifact(f *cleanup.FileInfo) bool {
	if !f.Tracked || f.IsDir {
		return false
	}
	// Dev artifacts are loose scratch files, not nested reference docs.
	// Only match files at most 2 levels deep (e.g., "looper.md" or "scripts/flow-state.json").
	if strings.Count(f.RelPath, "/") > 1 {
		return false
	}
	name := filepath.Base(f.RelPath)

	for _, p := range devArtifactPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	for _, s := range devArtifactSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

func isMisplacedDoc(f *cleanup.FileInfo) bool {
	if f.IsDir || !f.Tracked {
		return false
	}
	name := filepath.Base(f.RelPath)
	if !strings.HasSuffix(name, ".md") {
		return false
	}
	if strings.Contains(f.RelPath, "/") {
		return false
	}
	if allowedRootMD[name] {
		return false
	}
	return true
}

func isUntrackCandidate(f *cleanup.FileInfo) (bool, string) {
	if !f.Tracked || f.IsDir || f.IsSymlink {
		return false, ""
	}
	name := filepath.Base(f.RelPath)
	ext := filepath.Ext(name)

	if untrackExts[ext] {
		return true, "binary/archive extension"
	}
	// Image files at repo root should not be tracked.
	if imageExts[ext] && !strings.Contains(f.RelPath, "/") {
		return true, "image file at repo root"
	}
	if name == ".env" || (strings.HasPrefix(name, ".env.") &&
		!strings.HasSuffix(name, ".example") &&
		!strings.HasSuffix(name, ".sample") &&
		!strings.HasSuffix(name, ".template")) {
		return true, "environment file"
	}
	for _, dir := range untrackDirs {
		if strings.HasPrefix(f.RelPath, dir) {
			return true, "build output directory"
		}
	}
	// Executable with no extension = likely compiled binary.
	// Skip project toolchain dirs (scripts/, hooks/) where tracked binaries are intentional.
	if f.Executable && ext == "" &&
		!strings.HasPrefix(f.RelPath, "scripts/") &&
		!strings.HasPrefix(f.RelPath, "hooks/") {
		return true, "compiled binary"
	}
	if f.Content == cleanup.ContentGenerated {
		return true, "generated content"
	}
	return false, ""
}

// normalizeDocName returns the kebab-case lowercase version of a filename.
// Returns "" if the name is already normalized.
func normalizeDocName(name string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	normalized := strings.ToLower(base)
	normalized = strings.ReplaceAll(normalized, "_", "-")
	normalized += strings.ToLower(ext)
	if normalized == name {
		return ""
	}
	return normalized
}

func needsDocRename(f *cleanup.FileInfo) (string, bool) {
	if f.IsDir || !strings.HasPrefix(f.RelPath, "docs/") {
		return "", false
	}
	name := filepath.Base(f.RelPath)
	if norm := normalizeDocName(name); norm != "" {
		return filepath.Join(filepath.Dir(f.RelPath), norm), true
	}
	return "", false
}

func isMisplacedScript(f *cleanup.FileInfo) bool {
	if f.IsDir || f.IsSymlink {
		return false
	}
	return strings.HasSuffix(f.RelPath, ".sh") && !strings.Contains(f.RelPath, "/")
}

func findReferencedScripts(root string, filenames []string) map[string]bool {
	if len(filenames) == 0 {
		return nil
	}
	escaped := make([]string, len(filenames))
	for i, f := range filenames {
		escaped[i] = regexp.QuoteMeta(f)
	}
	pattern := strings.Join(escaped, "|")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "grep", "-rohE",
		"--include=*.go", "--include=*.md", "--include=*.yml",
		"--include=*.yaml", "--include=Makefile",
		pattern, ".")
	cmd.Dir = root
	out, _ := cmd.Output()

	result := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		for _, f := range filenames {
			if line == f {
				result[f] = true
			}
		}
	}
	return result
}

// findGoBinLinked returns the set of absolute paths that ~/go/bin/ symlinks point to.
// Any file at one of these paths is a live system binary and must not be touched.
func findGoBinLinked() map[string]bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	goBin := filepath.Join(home, "go", "bin")
	entries, err := os.ReadDir(goBin)
	if err != nil {
		return nil
	}
	targets := make(map[string]bool)
	for _, e := range entries {
		link := filepath.Join(goBin, e.Name())
		target, err := filepath.EvalSymlinks(link)
		if err != nil {
			continue
		}
		// Only record if it's actually a symlink (target differs from link path).
		if target != link {
			targets[target] = true
		}
	}
	return targets
}

func boolPtr(b bool) *bool { return &b }

func ignoredFileReason(name, ext string, f *cleanup.FileInfo) string {
	switch {
	case f.Executable && ext == "":
		return "ignored compiled binary"
	case f.Executable && ext == ".test":
		return "ignored test binary"
	case strings.HasPrefix(name, "coverage") || ext == ".out":
		return "ignored coverage output"
	case ext == ".html":
		return "ignored generated file"
	case ext == ".md":
		return "ignored dev document"
	case ext == ".sh":
		return "ignored script"
	default:
		return "ignored file on disk"
	}
}

func categorize(files []cleanup.FileInfo, cfg cleanup.Config) cleanup.ScanResult {
	result := cleanup.ScanResult{
		DeleteCandidates:      []cleanup.FileCandidate{},
		DevArtifactCandidates: []cleanup.FileCandidate{},
		ArchiveCandidates:     []cleanup.FileCandidate{},
		BrokenLinks:           []cleanup.FileCandidate{},
		LargeFiles:            []cleanup.FileCandidate{},
		MisplacedScripts:      []cleanup.FileCandidate{},
		MisplacedDocs:         []cleanup.FileCandidate{},
		UntrackCandidates:     []cleanup.FileCandidate{},
		RenameDocs:            []cleanup.FileCandidate{},
		AllFiles:              []cleanup.LabeledFile{},
		Summary:               map[string]int{},
	}

	// Pre-scan: collect misplaced script filenames for batch grep
	var scriptNames []string
	for i := range files {
		if isMisplacedScript(&files[i]) {
			scriptNames = append(scriptNames, filepath.Base(files[i].RelPath))
		}
	}
	referencedScripts := findReferencedScripts(cfg.Path, scriptNames)
	goBinLinked := findGoBinLinked()

	addResult := func(slice *[]cleanup.FileCandidate, c cleanup.FileCandidate, status, reason string) {
		*slice = append(*slice, c)
		result.AllFiles = append(result.AllFiles, cleanup.LabeledFile{
			File: c.File, Status: status, Reason: reason, SizeKB: c.SizeKB,
		})
	}

	for i := range files {
		f := &files[i]

		name := filepath.Base(f.RelPath)
		ext := filepath.Ext(name)

		// Skip live system binaries (~/go/bin/ symlinks to this file).
		if goBinLinked[f.Path] {
			continue
		}

		// Skip dotfiles and dotdirs unless whitelisted.
		// Only check dot-entries we have specific concerns about.
		// Exception: .looper* remnants are always caught and deleted.
		isDot := strings.HasPrefix(name, ".") || strings.HasPrefix(f.RelPath, ".")
		isLooper := strings.HasPrefix(name, ".looper")
		if isDot && !isLooper && !dotfileWhitelist[name] && !strings.HasPrefix(name, ".env.") {
			continue
		}

		// Ignored files: selectively clean dev junk, skip expected runtime data.
		if f.Ignored {
			if f.IsDir {
				continue
			}
			// Files in safe dirs (data/, bin/ primary, cache/) — always skip.
			inSafeDir := false
			for _, sd := range ignoredSafeDirs {
				if strings.HasPrefix(f.RelPath, sd) {
					inSafeDir = true
					break
				}
			}
			if inSafeDir {
				// Exception: non-primary files in bin/ (primary is protected by goBinLinked).
				if strings.HasPrefix(f.RelPath, "bin/") && ext == "" {
					addResult(&result.DeleteCandidates, cleanup.FileCandidate{
						File: f.RelPath, Reason: "ignored stale binary", SizeKB: f.Size / 1024,
					}, cleanup.StatusDelete, "ignored stale binary")
				}
				continue
			}
			// Backup files (*.backup.*)
			if strings.Contains(name, ".backup.") {
				addResult(&result.DeleteCandidates, cleanup.FileCandidate{
					File: f.RelPath, Reason: "ignored backup file", SizeKB: f.Size / 1024,
				}, cleanup.StatusDelete, "ignored backup file")
				continue
			}
			// Dev doc patterns at any depth
			isDevDoc := false
			for _, suf := range ignoredDevDocSuffixes {
				if strings.HasSuffix(name, suf) {
					isDevDoc = true
					break
				}
			}
			if isDevDoc {
				addResult(&result.DeleteCandidates, cleanup.FileCandidate{
					File: f.RelPath, Reason: "ignored dev document", SizeKB: f.Size / 1024,
				}, cleanup.StatusDelete, "ignored dev document")
				continue
			}
			// Verify scripts, archive files at root
			isRootLevel := !strings.Contains(f.RelPath, "/")
			if isRootLevel {
				for _, pfx := range ignoredDeletePrefixes {
					if strings.HasPrefix(name, pfx) {
						addResult(&result.DeleteCandidates, cleanup.FileCandidate{
							File: f.RelPath, Reason: "ignored dev script", SizeKB: f.Size / 1024,
						}, cleanup.StatusDelete, "ignored dev script")
						isDevDoc = true // reuse flag to skip
						break
					}
				}
				if isDevDoc {
					continue
				}
				// Temp files at root → delete
				if deleteExts[ext] {
					addResult(&result.DeleteCandidates, cleanup.FileCandidate{
						File: f.RelPath, Reason: "ignored temp file", SizeKB: f.Size / 1024,
					}, cleanup.StatusDelete, "ignored temp file")
					continue
				}
				// Archive files at root → move to .work/
				if archiveExts[ext] {
					addResult(&result.ArchiveCandidates, cleanup.FileCandidate{
						File:   f.RelPath,
						Reason: "archive file at repo root",
						SizeKB: f.Size / 1024,
						Score:  cleanup.Score(f),
					}, cleanup.StatusArchive, "archive file at repo root")
					continue
				}
			}
			// Everything else ignored — skip.
			continue
		}

		// 0. Migration files are source code, not data artifacts — always clean.
		if !f.IsDir && (strings.Contains(f.RelPath, "/migrations/") || strings.HasPrefix(f.RelPath, "migrations/")) {
			result.AllFiles = append(result.AllFiles, cleanup.LabeledFile{
				File: f.RelPath, Status: cleanup.StatusClean, SizeKB: f.Size / 1024,
			})
			continue
		}

		// 1. Data files — NEVER delete, always archive with warning
		if !f.IsDir && dataExts[ext] {
			reason := "⚠ DATA FILE — may contain important data, review before removing"
			if f.Tracked {
				reason = "⚠ DATA FILE (tracked) — may contain important data"
			}
			addResult(&result.ArchiveCandidates, cleanup.FileCandidate{
				File:        f.RelPath,
				Reason:      reason,
				SizeKB:      f.Size / 1024,
				Score:       cleanup.Score(f),
				ContentHint: "data",
			}, cleanup.StatusArchive, reason)
			continue
		}

		// 1b. Scaffold remnants (tracked files from project init with zero references)
		if f.Tracked && scaffoldFiles[f.RelPath] {
			addResult(&result.DeleteCandidates, cleanup.FileCandidate{
				File:   f.RelPath,
				Reason: "scaffold remnant",
				SizeKB: f.Size / 1024,
				Score:  cleanup.Score(f),
			}, cleanup.StatusDelete, "scaffold remnant")
			continue
		}

		// 2. Delete candidates
		if ok, reason := isDeleteCandidate(f); ok {
			addResult(&result.DeleteCandidates, cleanup.FileCandidate{
				File:   f.RelPath,
				Reason: reason,
				SizeKB: f.Size / 1024,
				Score:  cleanup.Score(f),
			}, cleanup.StatusDelete, reason)
			continue
		}

		// 2. Broken links
		if f.IsSymlink && f.LinkTarget != "" {
			addResult(&result.BrokenLinks, cleanup.FileCandidate{
				File:   f.RelPath,
				SizeKB: f.Size / 1024,
				Target: f.LinkTarget,
				Score:  cleanup.Score(f),
			}, cleanup.StatusBrokenLink, "broken symlink → "+f.LinkTarget)
			continue
		}

		// 3. Large files (>10MB), not matching delete patterns
		if f.Size > 10*1024*1024 && !f.IsDir {
			addResult(&result.LargeFiles, cleanup.FileCandidate{
				File:    f.RelPath,
				SizeKB:  f.Size / 1024,
				Tracked: boolPtr(f.Tracked),
				Score:   cleanup.Score(f),
			}, cleanup.StatusLargeFile, "file > 10MB")
			continue
		}

		// 4. Dev artifacts (tracked files matching patterns)
		if isDevArtifact(f) {
			addResult(&result.DevArtifactCandidates, cleanup.FileCandidate{
				File:   f.RelPath,
				Reason: "tracked dev artifact",
				SizeKB: f.Size / 1024,
				Score:  cleanup.Score(f),
			}, cleanup.StatusDevArtifact, "tracked dev artifact")
			continue
		}

		// 5. Misplaced docs (tracked .md at repo root, not allowlisted)
		if isMisplacedDoc(f) {
			addResult(&result.MisplacedDocs, cleanup.FileCandidate{
				File:   f.RelPath,
				Reason: "root .md → docs/",
				SizeKB: f.Size / 1024,
				Score:  cleanup.Score(f),
			}, cleanup.StatusMisplacedDoc, "root .md → docs/")
			continue
		}

		// 5b. Docs with non-normalized filenames (uppercase, underscores)
		if newPath, ok := needsDocRename(f); ok {
			addResult(&result.RenameDocs, cleanup.FileCandidate{
				File:   f.RelPath,
				Target: newPath,
				SizeKB: f.Size / 1024,
			}, cleanup.StatusRenameDoc, "rename → "+newPath)
			continue
		}

		// 6. Misplaced scripts (.sh at repo root level)
		if isMisplacedScript(f) {
			referenced := referencedScripts[name]
			addResult(&result.MisplacedScripts, cleanup.FileCandidate{
				File:       f.RelPath,
				SizeKB:     f.Size / 1024,
				Tracked:    boolPtr(f.Tracked),
				Referenced: boolPtr(referenced),
				Score:      cleanup.Score(f),
			}, cleanup.StatusMisplacedScript, "")
			continue
		}

		// 7. Untrack candidates (tracked files that shouldn't be in git)
		if ok, reason := isUntrackCandidate(f); ok {
			addResult(&result.UntrackCandidates, cleanup.FileCandidate{
				File:   f.RelPath,
				Reason: reason,
				SizeKB: f.Size / 1024,
				Score:  cleanup.Score(f),
			}, cleanup.StatusUntrack, reason)
			continue
		}

		// 8. Archive candidates (untracked, not caught above)
		if !f.Tracked && !f.IsDir {
			hint := f.Content.String()
			reason := "untracked file"
			if f.Duplicate != "" {
				reason += ", potential duplicate of " + f.Duplicate
			}

			c := cleanup.FileCandidate{
				File:   f.RelPath,
				Reason: reason,
				SizeKB: f.Size / 1024,
				Score:  cleanup.Score(f),
			}
			if hint != "unknown" && hint != "meaningful" {
				c.ContentHint = hint
			}
			if f.StaleDays > 0 {
				c.StaleDays = f.StaleDays
			}
			addResult(&result.ArchiveCandidates, c, cleanup.StatusArchive, reason)
			continue
		}

		// Skip directories from AllFiles — they're structural, not interesting.
		if f.IsDir {
			continue
		}

		// No action needed — file is clean.
		result.AllFiles = append(result.AllFiles, cleanup.LabeledFile{
			File: f.RelPath, Status: cleanup.StatusClean, SizeKB: f.Size / 1024,
		})
	}

	result.Summary[cleanup.CatDelete] = len(result.DeleteCandidates)
	result.Summary[cleanup.CatDevArtifact] = len(result.DevArtifactCandidates)
	result.Summary[cleanup.CatArchive] = len(result.ArchiveCandidates)
	result.Summary[cleanup.CatBrokenLink] = len(result.BrokenLinks)
	result.Summary[cleanup.CatLargeFile] = len(result.LargeFiles)
	result.Summary[cleanup.CatMisplaced] = len(result.MisplacedScripts)
	result.Summary[cleanup.CatMisplacedDoc] = len(result.MisplacedDocs)
	result.Summary[cleanup.CatUntrack] = len(result.UntrackCandidates)
	result.Summary[cleanup.CatRenameDocs] = len(result.RenameDocs)
	result.Summary["total"] = len(result.DeleteCandidates) + len(result.DevArtifactCandidates) +
		len(result.ArchiveCandidates) + len(result.BrokenLinks) +
		len(result.LargeFiles) + len(result.MisplacedScripts) + len(result.MisplacedDocs) +
		len(result.UntrackCandidates) + len(result.RenameDocs)

	return result
}

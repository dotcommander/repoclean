package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dotcommander/repoclean/internal/cleanup"
)

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

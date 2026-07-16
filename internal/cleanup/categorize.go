package cleanup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	// Dotfiles/dotdirs we actually care about — everything else is skipped.
	dotfileWhitelist = map[string]bool{
		".DS_Store":     true,
		".env":          true,
		".pytest_cache": true,
		".mypy_cache":   true,
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
	// Data files: NEVER delete. Route to archive with prominent warning.
	dataExts = map[string]bool{
		".db": true, ".sqlite": true, ".sqlite3": true,
		".bak": true, ".sql": true, ".csv": true,
	}
	deleteDirs = map[string]bool{
		"node_modules": true, "__pycache__": true,
		".pytest_cache": true, ".mypy_cache": true,
	}
	// Extensions that typically shouldn't be tracked in git.
	untrackExts = map[string]bool{
		".exe": true, ".dll": true, ".so": true, ".dylib": true,
		".o": true, ".a": true, ".obj": true, ".lib": true,
		".zip": true, ".tar": true, ".gz": true, ".tgz": true,
		".rar": true, ".7z": true, ".wasm": true,
	}
)

func isDeleteCandidate(f *FileInfo) (bool, string) {
	name := filepath.Base(f.RelPath)

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

	if f.HasFinding(RuleEmpty) {
		return true, "empty directory"
	}

	return false, ""
}

func isDevArtifact(f *FileInfo, rules Rules) bool {
	if !f.Tracked || f.IsDir {
		return false
	}
	// Dev artifacts are loose scratch files, not nested reference docs.
	// Only match files at most 2 levels deep (e.g., "draft.md" or "scripts/scratch-state.json").
	if strings.Count(f.RelPath, "/") > 1 {
		return false
	}
	name := filepath.Base(f.RelPath)

	for _, p := range rules.DevArtifactPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	for _, s := range rules.DevArtifactSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

func isMisplacedDoc(f *FileInfo, allowedMD map[string]bool) bool {
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
	if allowedMD[name] {
		return false
	}
	return true
}

func isUntrackCandidate(f *FileInfo, untrackDirList []string) (bool, string) {
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
	for _, dir := range untrackDirList {
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
	if f.HasFinding(RuleGenerated) {
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

func needsDocRename(f *FileInfo) (string, bool) {
	if f.IsDir || !strings.HasPrefix(f.RelPath, "docs/") {
		return "", false
	}
	name := filepath.Base(f.RelPath)
	// README.md is conventional uppercase — never rename.
	if strings.EqualFold(name, "readme.md") {
		return "", false
	}
	// _test.go files follow Go convention — never rename.
	if strings.HasSuffix(name, "_test.go") {
		return "", false
	}
	if norm := normalizeDocName(name); norm != "" {
		return filepath.Join(filepath.Dir(f.RelPath), norm), true
	}
	return "", false
}

func isMisplacedScript(f *FileInfo) bool {
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

func Categorize(files []FileInfo, cfg Config) ScanResult {
	if cfg.Rules.IsZero() {
		cfg.Rules = DefaultRules()
	}

	allowedMD := toSet(cfg.Rules.AllowedRootMD)
	scaffolds := toSet(cfg.Rules.ScaffoldFiles)

	result := ScanResult{
		DeleteCandidates:      []FileCandidate{},
		DevArtifactCandidates: []FileCandidate{},
		ArchiveCandidates:     []FileCandidate{},
		BrokenLinks:           []FileCandidate{},
		LargeFiles:            []FileCandidate{},
		MisplacedScripts:      []FileCandidate{},
		MisplacedDocs:         []FileCandidate{},
		UntrackCandidates:     []FileCandidate{},
		RenameDocs:            []FileCandidate{},
		AllFiles:              []LabeledFile{},
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

	addResult := func(slice *[]FileCandidate, c FileCandidate, findings []Finding, status, reason string) {
		if len(findings) > 0 {
			c.Findings = findings
		}
		*slice = append(*slice, c)
		result.AllFiles = append(result.AllFiles, LabeledFile{
			File: c.File, Status: status, Reason: reason, SizeKB: c.SizeKB,
		})
	}

	for i := range files {
		f := &files[i]

		// Skip files suppressed by .repocleanignore.
		if f.Suppressed {
			result.AllFiles = append(result.AllFiles, LabeledFile{
				File: f.RelPath, Status: StatusClean, Reason: "suppressed", SizeKB: f.Size / 1024,
			})
			continue
		}
		if f.GitStateUnknown {
			result.AllFiles = append(result.AllFiles, LabeledFile{
				File: f.RelPath, Status: StatusClean, Reason: "repository state unknown", SizeKB: f.Size / 1024,
			})
			continue
		}

		name := filepath.Base(f.RelPath)
		ext := filepath.Ext(name)

		// Skip live system binaries (~/go/bin/ symlinks to this file).
		if goBinLinked[f.Path] {
			continue
		}

		// Skip dotfiles and dotdirs unless whitelisted.
		// Only check dot-entries we have specific concerns about.
		isDot := strings.HasPrefix(name, ".") || strings.HasPrefix(f.RelPath, ".")
		if isDot && !dotfileWhitelist[name] && !strings.HasPrefix(name, ".env.") {
			continue
		}

		// Ignored files: selectively clean dev junk, skip expected runtime data.
		if f.Ignored {
			if f.IsDir {
				continue
			}
			if deleteNames[name] {
				addResult(&result.DeleteCandidates, FileCandidate{
					File: f.RelPath, Reason: "ignored system file", SizeKB: f.Size / 1024,
				}, f.Findings, StatusDelete, "ignored system file")
				continue
			}
			// Files in safe dirs (data/, bin/ primary, cache/) — always skip.
			inSafeDir := false
			for _, sd := range cfg.Rules.IgnoredSafeDirs {
				if strings.HasPrefix(f.RelPath, sd) {
					inSafeDir = true
					break
				}
			}
			if inSafeDir {
				// Exception: non-primary files in bin/ (primary is protected by goBinLinked).
				if strings.HasPrefix(f.RelPath, "bin/") && ext == "" {
					addResult(&result.DeleteCandidates, FileCandidate{
						File: f.RelPath, Reason: "ignored stale binary", SizeKB: f.Size / 1024,
					}, f.Findings, StatusDelete, "ignored stale binary")
				}
				continue
			}
			// Backup files (*.backup.*)
			if strings.Contains(name, ".backup.") {
				addResult(&result.DeleteCandidates, FileCandidate{
					File: f.RelPath, Reason: "ignored backup file", SizeKB: f.Size / 1024,
				}, f.Findings, StatusDelete, "ignored backup file")
				continue
			}
			// Dev doc patterns at any depth
			isDevDoc := false
			for _, suf := range cfg.Rules.IgnoredDevDocSuffixes {
				if strings.HasSuffix(name, suf) {
					isDevDoc = true
					break
				}
			}
			if isDevDoc {
				addResult(&result.DeleteCandidates, FileCandidate{
					File: f.RelPath, Reason: "ignored dev document", SizeKB: f.Size / 1024,
				}, f.Findings, StatusDelete, "ignored dev document")
				continue
			}
			// Verify scripts, archive files at root
			isRootLevel := !strings.Contains(f.RelPath, "/")
			if isRootLevel {
				isDeletePrefix := false
				for _, pfx := range cfg.Rules.IgnoredDeletePrefixes {
					if strings.HasPrefix(name, pfx) {
						addResult(&result.DeleteCandidates, FileCandidate{
							File: f.RelPath, Reason: "ignored dev script", SizeKB: f.Size / 1024,
						}, f.Findings, StatusDelete, "ignored dev script")
						isDeletePrefix = true
						break
					}
				}
				if isDeletePrefix {
					continue
				}
				// Temp files at root → delete
				if deleteExts[ext] {
					addResult(&result.DeleteCandidates, FileCandidate{
						File: f.RelPath, Reason: "ignored temp file", SizeKB: f.Size / 1024,
					}, f.Findings, StatusDelete, "ignored temp file")
					continue
				}
				// Archive files at root → move to .work/
				if archiveExts[ext] {
					addResult(&result.ArchiveCandidates, FileCandidate{
						File:   f.RelPath,
						Reason: "archive file at repo root",
						SizeKB: f.Size / 1024,
						Score:  Score(f),
					}, f.Findings, StatusArchive, "archive file at repo root")
					continue
				}
			}
			// Everything else ignored — skip.
			continue
		}

		// 0. Migration files are source code, not data artifacts — always clean.
		if !f.IsDir && (strings.Contains(f.RelPath, "/migrations/") || strings.HasPrefix(f.RelPath, "migrations/")) {
			result.AllFiles = append(result.AllFiles, LabeledFile{
				File: f.RelPath, Status: StatusClean, SizeKB: f.Size / 1024,
			})
			continue
		}

		// 1. Data files — NEVER delete, always archive with warning
		if !f.IsDir && dataExts[ext] {
			reason := "⚠ DATA FILE — may contain important data, review before removing"
			if f.Tracked {
				reason = "⚠ DATA FILE (tracked) — may contain important data"
			}
			addResult(&result.ArchiveCandidates, FileCandidate{
				File:        f.RelPath,
				Reason:      reason,
				SizeKB:      f.Size / 1024,
				Score:       Score(f),
				ContentHint: "data",
			}, f.Findings, StatusArchive, reason)
			continue
		}

		// 1b. Scaffold remnants (tracked files from project init with zero references)
		if f.Tracked && scaffolds[f.RelPath] {
			addResult(&result.DeleteCandidates, FileCandidate{
				File:   f.RelPath,
				Reason: "scaffold remnant",
				SizeKB: f.Size / 1024,
				Score:  Score(f),
			}, f.Findings, StatusDelete, "scaffold remnant")
			continue
		}

		// 2. Delete candidates
		if ok, reason := isDeleteCandidate(f); ok {
			addResult(&result.DeleteCandidates, FileCandidate{
				File:   f.RelPath,
				Reason: reason,
				SizeKB: f.Size / 1024,
				Score:  Score(f),
			}, f.Findings, StatusDelete, reason)
			continue
		}

		// 3. Broken links
		if f.IsSymlink && f.LinkTarget != "" {
			addResult(&result.BrokenLinks, FileCandidate{
				File:   f.RelPath,
				SizeKB: f.Size / 1024,
				Target: f.LinkTarget,
				Score:  Score(f),
			}, f.Findings, StatusBrokenLink, "broken symlink → "+f.LinkTarget)
			continue
		}

		// 4. Large files (>10MB), not matching delete patterns
		if f.Size > 10*1024*1024 && !f.IsDir {
			addResult(&result.LargeFiles, FileCandidate{
				File:    f.RelPath,
				SizeKB:  f.Size / 1024,
				Tracked: boolPtr(f.Tracked),
				Score:   Score(f),
			}, f.Findings, StatusLargeFile, "file > 10MB")
			continue
		}

		// 5. Dev artifacts (tracked files matching patterns)
		if isDevArtifact(f, cfg.Rules) {
			addResult(&result.DevArtifactCandidates, FileCandidate{
				File:   f.RelPath,
				Reason: "tracked dev artifact",
				SizeKB: f.Size / 1024,
				Score:  Score(f),
			}, f.Findings, StatusDevArtifact, "tracked dev artifact")
			continue
		}

		// 6. Misplaced docs (tracked .md at repo root, not allowlisted)
		if isMisplacedDoc(f, allowedMD) {
			addResult(&result.MisplacedDocs, FileCandidate{
				File:   f.RelPath,
				Reason: "root .md → docs/",
				SizeKB: f.Size / 1024,
				Score:  Score(f),
			}, f.Findings, StatusMisplacedDoc, "root .md → docs/")
			continue
		}

		// 6b. Docs with non-normalized filenames (uppercase, underscores)
		if newPath, ok := needsDocRename(f); ok {
			addResult(&result.RenameDocs, FileCandidate{
				File:   f.RelPath,
				Target: newPath,
				SizeKB: f.Size / 1024,
			}, f.Findings, StatusRenameDoc, "rename → "+newPath)
			continue
		}

		// 7. Misplaced scripts (.sh at repo root level)
		if isMisplacedScript(f) {
			referenced := referencedScripts[name]
			addResult(&result.MisplacedScripts, FileCandidate{
				File:       f.RelPath,
				SizeKB:     f.Size / 1024,
				Tracked:    boolPtr(f.Tracked),
				Referenced: boolPtr(referenced),
				Score:      Score(f),
			}, f.Findings, StatusMisplacedScript, "")
			continue
		}

		// 8. Untrack candidates (tracked files that shouldn't be in git)
		if ok, reason := isUntrackCandidate(f, cfg.Rules.UntrackDirs); ok {
			addResult(&result.UntrackCandidates, FileCandidate{
				File:   f.RelPath,
				Reason: reason,
				SizeKB: f.Size / 1024,
				Score:  Score(f),
			}, f.Findings, StatusUntrack, reason)
			continue
		}

		// 9. Archive candidates (untracked, not caught above)
		if !f.GitStateUnknown && !f.Tracked && !f.IsDir {
			hint := f.Content.String()
			reason := "untracked file"
			if f.HasFinding(RuleDuplicate) {
				reason += ", potential duplicate of " + f.Duplicate
			}

			c := FileCandidate{
				File:   f.RelPath,
				Reason: reason,
				SizeKB: f.Size / 1024,
				Score:  Score(f),
			}
			if hint != "unknown" && hint != "meaningful" {
				c.ContentHint = hint
			}
			if f.StaleDays > 0 {
				c.StaleDays = f.StaleDays
			}
			addResult(&result.ArchiveCandidates, c, f.Findings, StatusArchive, reason)
			continue
		}

		// Skip directories from AllFiles — they're structural, not interesting.
		if f.IsDir {
			continue
		}

		// No action needed — file is clean.
		result.AllFiles = append(result.AllFiles, LabeledFile{
			File: f.RelPath, Status: StatusClean, SizeKB: f.Size / 1024,
		})
	}

	result.Summary[CatDelete] = len(result.DeleteCandidates)
	result.Summary[CatDevArtifact] = len(result.DevArtifactCandidates)
	result.Summary[CatArchive] = len(result.ArchiveCandidates)
	result.Summary[CatBrokenLink] = len(result.BrokenLinks)
	result.Summary[CatLargeFile] = len(result.LargeFiles)
	result.Summary[CatMisplaced] = len(result.MisplacedScripts)
	result.Summary[CatMisplacedDoc] = len(result.MisplacedDocs)
	result.Summary[CatUntrack] = len(result.UntrackCandidates)
	result.Summary[CatRenameDocs] = len(result.RenameDocs)
	result.Summary["total"] = len(result.DeleteCandidates) + len(result.DevArtifactCandidates) +
		len(result.ArchiveCandidates) + len(result.BrokenLinks) +
		len(result.LargeFiles) + len(result.MisplacedScripts) + len(result.MisplacedDocs) +
		len(result.UntrackCandidates) + len(result.RenameDocs)

	result.HealthScore = CalculateHealth(result)

	return result
}

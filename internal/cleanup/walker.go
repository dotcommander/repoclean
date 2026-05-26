package cleanup

import (
	"bufio"
	"bytes"
	"context"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// defaultGitTimeout bounds `git ls-files` probes so a wedged or hanging git
// invocation cannot stall a repoclean scan. Set generous: 30s covers very
// large repos while still failing fast if git is hung.
const defaultGitTimeout = 30 * time.Second

// skipDirs are directories we add as entries but do not descend into.
var skipDirs = map[string]bool{
	"node_modules":  true,
	"__pycache__":   true,
	".pytest_cache": true,
	".mypy_cache":   true,
}

// nestedRepos tracks directories that contain their own .git (nested repos).
// Detected during walk and skipped from results in post-processing.
// We can't skip during WalkDir because .git is seen after the parent.

// Walk returns enriched FileInfo entries for all files and selected directories
// under cfg.Path up to cfg.MaxDepth levels deep.
func Walk(cfg Config) ([]FileInfo, error) {
	root := cfg.Path
	rootDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))

	var files []FileInfo
	// dirs tracks all directory paths collected during the walk.
	dirs := make(map[string]bool)
	// dirsWithChildren tracks directories that have at least one child seen.
	dirsWithChildren := make(map[string]bool)
	// nestedRepos tracks directories containing their own .git (nested repos).
	nestedRepos := make(map[string]bool)

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			log.Println("repoclean: walk:", err)
			return nil
		}

		// Skip .git entirely, and record nested repos (not the root repo).
		if d.IsDir() && d.Name() == ".git" {
			parent := filepath.Dir(path)
			if parent != root {
				nestedRepos[parent] = true
			}
			return fs.SkipDir
		}

		// Depth check (root itself is depth 0).
		depth := strings.Count(filepath.Clean(path), string(os.PathSeparator)) - rootDepth
		if depth > cfg.MaxDepth {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		// Skip the root entry itself.
		if path == root {
			return nil
		}

		// Mark the parent as having children.
		parent := filepath.Dir(path)
		dirsWithChildren[parent] = true

		relPath, relErr := filepath.Rel(root, path)
		if relErr != nil {
			log.Println("repoclean: walk:", relErr)
			relPath = path
		}

		fi := FileInfo{
			Path:    path,
			RelPath: relPath,
			IsDir:   d.IsDir(),
		}

		// Detect symlinks before calling d.Info() so Lstat takes precedence.
		if d.Type()&fs.ModeSymlink != 0 {
			fi.IsSymlink = true
			lstat, lstatErr := os.Lstat(path)
			if lstatErr == nil {
				fi.Size = lstat.Size()
				fi.ModTime = lstat.ModTime()
			}
			target, readErr := os.Readlink(path)
			if readErr == nil {
				// Check whether target resolves.
				if _, statErr := os.Stat(path); statErr != nil {
					fi.LinkTarget = target
				}
			}
		} else {
			info, infoErr := d.Info()
			if infoErr != nil {
				log.Println("repoclean: walk:", infoErr)
			} else {
				fi.Size = info.Size()
				fi.ModTime = info.ModTime()
				fi.Executable = info.Mode()&0o111 != 0
			}
		}

		if d.IsDir() {
			dirs[path] = true
		}

		files = append(files, fi)

		// Add skip-dir entries but don't descend.
		if d.IsDir() && skipDirs[d.Name()] {
			return fs.SkipDir
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	// Filter out files under nested repos.
	if len(nestedRepos) > 0 {
		filtered := files[:0]
		for _, f := range files {
			if isUnderNestedRepo(f.Path, nestedRepos) {
				continue
			}
			filtered = append(filtered, f)
		}
		files = filtered
	}

	// Mark empty directories: any dir with no children seen during the walk.
	// Only mark as empty if we fully scanned the directory (not at maxDepth).
	// Directories at maxDepth may have children we didn't see.
	for i := range files {
		if !files[i].IsDir || !dirs[files[i].Path] || dirsWithChildren[files[i].Path] {
			continue
		}
		depth := strings.Count(filepath.Clean(files[i].Path), string(os.PathSeparator)) - rootDepth
		if depth >= cfg.MaxDepth {
			continue // can't know if truly empty — children were skipped
		}
		files[i].IsEmpty = true
	}

	markTracked(cfg.Path, files)
	patterns := loadIgnorePatterns(cfg.Path)
	markSuppressed(files, patterns)

	return files, nil
}

// loadIgnorePatterns reads .repocleanignore from root and returns glob patterns.
// Format: one pattern per line, # comments, blank lines skipped.
func loadIgnorePatterns(root string) []string {
	f, err := os.Open(filepath.Join(root, ".repocleanignore"))
	if err != nil {
		return nil
	}
	defer f.Close()

	var patterns []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns
}

// markSuppressed sets Suppressed=true on files matching .repocleanignore patterns.
func markSuppressed(files []FileInfo, patterns []string) {
	if len(patterns) == 0 {
		return
	}
	for i := range files {
		for _, pattern := range patterns {
			if matchesIgnorePattern(pattern, files[i].RelPath) {
				files[i].Suppressed = true
				break
			}
		}
	}
}

func matchesIgnorePattern(pattern, relPath string) bool {
	dirPattern := strings.HasSuffix(pattern, "/") || strings.HasSuffix(pattern, string(os.PathSeparator))
	pattern = filepath.Clean(pattern)
	if dirPattern {
		prefix := strings.TrimSuffix(pattern, string(os.PathSeparator))
		return relPath == prefix || strings.HasPrefix(relPath, prefix+string(os.PathSeparator))
	}
	if matched, err := filepath.Match(pattern, relPath); err == nil && matched {
		return true
	}
	// Also match against just the filename for simple patterns like "*.log".
	if matched, err := filepath.Match(pattern, filepath.Base(relPath)); err == nil && matched {
		return true
	}
	return false
}

// isUnderNestedRepo checks if path is inside any nested repo directory.
func isUnderNestedRepo(path string, repos map[string]bool) bool {
	for repo := range repos {
		if strings.HasPrefix(path, repo+string(os.PathSeparator)) || path == repo {
			return true
		}
	}
	return false
}

// gitFileSet runs a git ls-files command and returns the output lines as an
// absolute-path set. Returns nil on error (caller treats absence as unknown).
func gitFileSet(ctx context.Context, root string, args ...string) map[string]bool {
	cmd := exec.CommandContext(ctx, "git", append([]string{"ls-files"}, args...)...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	set := make(map[string]bool)
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			set[filepath.Join(root, line)] = true
		}
	}
	return set
}

// markTracked runs git ls-files to identify tracked and ignored files.
// Three states: Tracked=true (in git), Ignored=true (matched .gitignore),
// or both false (untracked, visible to cleanup).
func markTracked(root string, files []FileInfo) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultGitTimeout)
	defer cancel()

	tracked := gitFileSet(ctx, root)
	if tracked == nil {
		return
	}
	ignored := gitFileSet(ctx, root, "--others", "--ignored", "--exclude-standard")

	for i := range files {
		if !files[i].IsDir {
			files[i].Tracked = tracked[files[i].Path]
			files[i].Ignored = ignored != nil && ignored[files[i].Path]
		}
	}
}

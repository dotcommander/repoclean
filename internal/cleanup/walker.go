package cleanup

import (
	"bufio"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

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

// WalkRepository walks using an already-opened repository view.
func WalkRepository(cfg Config, repo *Repository) ([]FileInfo, error) {
	root := cfg.Path
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
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
		if d.Name() == ".git" {
			parent := filepath.Dir(path)
			if parent != root {
				nestedRepos[parent] = true
			}
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
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

	if repo != nil {
		if err := repo.markTracked(files); err != nil {
			log.Printf("repoclean: repository tracked state skipped: %v", err)
			MarkRepositoryStateUnknown(files)
		}
	}
	patterns := loadIgnorePatterns(cfg.Path)
	markSuppressed(files, patterns)

	return files, nil
}

// MarkRepositoryStateUnknown suppresses cleanup decisions after repository
// metadata or ignore configuration was discovered but could not be read.
func MarkRepositoryStateUnknown(files []FileInfo) {
	for i := range files {
		if !files[i].IsDir {
			files[i].GitStateUnknown = true
		}
	}
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

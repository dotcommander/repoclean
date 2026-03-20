package cleanup

import (
	"bufio"
	"bytes"
	"io/fs"
	"log"
	"os"
	"os/exec"
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
			log.Println("walk warning:", err)
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
			log.Println("walk rel warning:", relErr)
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
				log.Println("walk info warning:", infoErr)
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

	return files, nil
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

// markTracked runs git ls-files to identify tracked and ignored files.
// Three states: Tracked=true (in git), Ignored=true (matched .gitignore),
// or both false (untracked, visible to cleanup).
func markTracked(root string, files []FileInfo) {
	// Get tracked files.
	cmd := exec.Command("git", "ls-files")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return
	}

	tracked := make(map[string]bool)
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			tracked[filepath.Join(root, line)] = true
		}
	}

	// Get ignored files.
	cmd2 := exec.Command("git", "ls-files", "--others", "--ignored", "--exclude-standard")
	cmd2.Dir = root
	out2, _ := cmd2.Output()

	ignored := make(map[string]bool)
	sc2 := bufio.NewScanner(bytes.NewReader(out2))
	for sc2.Scan() {
		if line := sc2.Text(); line != "" {
			ignored[filepath.Join(root, line)] = true
		}
	}

	for i := range files {
		if !files[i].IsDir {
			files[i].Tracked = tracked[files[i].Path]
			files[i].Ignored = ignored[files[i].Path]
		}
	}
}

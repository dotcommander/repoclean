package cleanup

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var backupSuffixes = []string{"-v2", "-old", "-backup", "-copy", "-draft", ".orig", ".backup"}

// commonMultiNames are filenames that conventionally appear in multiple directories
// within a project and are not meaningful duplicates.
var commonMultiNames = map[string]bool{
	"main.go": true, "main.ts": true, "main.py": true, "main.rs": true,
	"index.ts": true, "index.js": true, "index.tsx": true, "index.jsx": true,
	"index.html": true, "index.css": true,
	"README.md": true, "CHANGELOG.md": true, "LICENSE": true,
	"Makefile": true, "Taskfile.yml": true, "Dockerfile": true,
	".gitignore": true, ".env": true, ".env.example": true,
	"types.go": true, "types.ts": true, "utils.go": true, "utils.ts": true,
	"config.go": true, "config.ts": true, "config.yaml": true, "config.json": true,
	"test_helpers.go": true, "testdata": true,
}

// FindDuplicates detects duplicate files and sets FileInfo.Duplicate field.
// Two signals are used: backup suffix patterns and basename collisions by size.
func FindDuplicates(files []FileInfo) {
	byPath := make(map[string]*FileInfo, len(files))
	byBase := make(map[string][]*FileInfo)

	for i := range files {
		f := &files[i]
		if f.IsDir || f.IsSymlink {
			continue
		}
		byPath[f.RelPath] = f
		base := filepath.Base(f.RelPath)
		byBase[base] = append(byBase[base], f)
	}

	// Signal 1: backup suffix patterns
	for i := range files {
		f := &files[i]
		if f.IsDir || f.IsSymlink || f.Duplicate != "" {
			continue
		}
		if orig := findOriginal(f.RelPath, byPath); orig != "" {
			f.Duplicate = orig
		}
	}

	// Signal 2: basename collisions by size
	for baseName, group := range byBase {
		if len(group) < 2 {
			continue
		}
		// Skip common multi-instance filenames (main.go, index.ts, README.md, etc.)
		if commonMultiNames[baseName] {
			continue
		}
		bySizeMap := make(map[int64][]*FileInfo)
		for _, f := range group {
			if f.Duplicate == "" {
				bySizeMap[f.Size] = append(bySizeMap[f.Size], f)
			}
		}
		for _, matches := range bySizeMap {
			if len(matches) < 2 {
				continue
			}
			// Only consider files > 4KB; small files with the same name are
			// extremely common (boilerplate, config, entrypoints).
			if matches[0].Size <= 4096 {
				continue
			}
			markShortestAsDuplicate(matches)
		}
	}

	// Signal 3: content hash for same-size files with different names
	bySize := make(map[int64][]*FileInfo)
	for i := range files {
		f := &files[i]
		if f.IsDir || f.IsSymlink || f.Duplicate != "" || f.Size <= 4096 {
			continue
		}
		bySize[f.Size] = append(bySize[f.Size], f)
	}
	for _, group := range bySize {
		if len(group) < 2 {
			continue
		}
		byHash := make(map[string][]*FileInfo)
		for _, f := range group {
			h := hashPrefix(f.Path)
			if h != "" {
				byHash[h] = append(byHash[h], f)
			}
		}
		for _, matches := range byHash {
			if len(matches) < 2 {
				continue
			}
			markShortestAsDuplicate(matches)
		}
	}
}

// markShortestAsDuplicate picks the file with the shortest RelPath as the canonical
// copy and marks all others in the group as duplicates of it.
func markShortestAsDuplicate(matches []*FileInfo) {
	shortest := matches[0]
	for _, f := range matches[1:] {
		if len(f.RelPath) < len(shortest.RelPath) {
			shortest = f
		}
	}
	for _, f := range matches {
		if f != shortest && f.Duplicate == "" {
			f.Duplicate = shortest.RelPath
		}
	}
}

// hashPrefix returns hex SHA-256 of the first 4KB of a file.
func hashPrefix(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, 4096)); err != nil {
		return ""
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// findOriginal checks if rel has a backup suffix and returns the original relPath if found.
func findOriginal(rel string, byPath map[string]*FileInfo) string {
	dir := filepath.Dir(rel)
	base := filepath.Base(rel)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)

	for _, suffix := range backupSuffixes {
		if !strings.HasSuffix(stem, suffix) {
			continue
		}
		origStem := strings.TrimSuffix(stem, suffix)
		origBase := origStem + ext
		var origRel string
		if dir == "." {
			origRel = origBase
		} else {
			origRel = dir + "/" + origBase
		}
		if _, ok := byPath[origRel]; ok {
			return origRel
		}
	}
	return ""
}

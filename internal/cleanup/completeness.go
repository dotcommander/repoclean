package cleanup

import (
	"path/filepath"
	"strings"
)

// MissingItem represents a file or pattern that a healthy repo should have.
type MissingItem struct {
	Name     string `json:"name"`
	Severity string `json:"severity"` // "error", "warning", "info"
	Why      string `json:"why"`
}

// CompletenessReport holds the results of a repo completeness check.
type CompletenessReport struct {
	Path    string        `json:"path"`
	Missing []MissingItem `json:"missing"`
	Score   int           `json:"score"` // 0-100, 100 = fully complete
}

// expectedFile defines a file check with alternatives.
type expectedFile struct {
	names    []string // any of these satisfies the check
	severity string
	why      string
}

var expectedFiles = []expectedFile{
	{
		names:    []string{"LICENSE", "LICENSE.md", "LICENSE.txt", "LICENCE", "LICENCE.md"},
		severity: "error",
		why:      "no license file — repo has no legal clarity for contributors or users",
	},
	{
		names:    []string{".gitignore"},
		severity: "error",
		why:      "no .gitignore — build artifacts, IDE files, and secrets may be committed",
	},
	{
		names:    []string{"README.md", "README", "README.txt", "readme.md"},
		severity: "error",
		why:      "no README — repo has no entry point for new contributors",
	},
	{
		names:    []string{"CHANGELOG.md", "CHANGELOG", "CHANGES.md", "HISTORY.md"},
		severity: "warning",
		why:      "no changelog — release history is undocumented",
	},
	{
		names:    []string{"CONTRIBUTING.md", "CONTRIBUTING"},
		severity: "info",
		why:      "no contributing guide — contribution process is undocumented",
	},
	{
		names:    []string{"SECURITY.md", "SECURITY"},
		severity: "info",
		why:      "no security policy — vulnerability reporting process is undefined",
	},
}

// ciPatterns are glob patterns that indicate CI configuration exists.
// Checked against RelPath of all walked files.
var ciPatterns = []string{
	".github/workflows/*.yml",
	".github/workflows/*.yaml",
	".gitlab-ci.yml",
	"Jenkinsfile",
	".circleci/config.yml",
	".travis.yml",
	"bitbucket-pipelines.yml",
}

// buildPatterns indicate a build/task system exists.
var buildPatterns = []string{
	"Makefile",
	"Taskfile.yml",
	"Taskfile.yaml",
	"justfile",
	"build.gradle",
	"build.gradle.kts",
	"pom.xml",
	"CMakeLists.txt",
	"meson.build",
}

// CheckCompleteness analyzes walked files for missing healthy-repo files.
func CheckCompleteness(files []FileInfo) CompletenessReport {
	// Build a set of all relative paths for quick lookup.
	pathSet := make(map[string]bool, len(files))
	for _, f := range files {
		pathSet[f.RelPath] = true
	}

	var missing []MissingItem

	// Check expected files.
	for _, ef := range expectedFiles {
		found := false
		for _, name := range ef.names {
			if pathSet[name] {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, MissingItem{
				Name:     ef.names[0],
				Severity: ef.severity,
				Why:      ef.why,
			})
		}
	}

	// Check CI config.
	hasCI := false
	for _, f := range files {
		for _, pattern := range ciPatterns {
			if matched, _ := filepath.Match(pattern, f.RelPath); matched {
				hasCI = true
				break
			}
		}
		if hasCI {
			break
		}
	}
	if !hasCI {
		missing = append(missing, MissingItem{
			Name:     "CI config",
			Severity: "warning",
			Why:      "no CI/CD configuration — code is not automatically tested or deployed",
		})
	}

	// Check build/task system.
	hasBuild := false
	for _, f := range files {
		for _, pattern := range buildPatterns {
			if matched, _ := filepath.Match(pattern, f.RelPath); matched {
				hasBuild = true
				break
			}
			// Also check just the base name for patterns without directory.
			if !strings.Contains(pattern, "/") && filepath.Base(f.RelPath) == pattern {
				hasBuild = true
				break
			}
		}
		if hasBuild {
			break
		}
	}
	if !hasBuild {
		missing = append(missing, MissingItem{
			Name:     "Build system",
			Severity: "info",
			Why:      "no Makefile, Taskfile, or build config — build process is undocumented",
		})
	}

	// Calculate score: start at 100, deduct per missing item by severity.
	score := 100
	for _, m := range missing {
		switch m.Severity {
		case "error":
			score -= 20
		case "warning":
			score -= 10
		case "info":
			score -= 5
		}
	}
	if score < 0 {
		score = 0
	}

	return CompletenessReport{
		Missing: missing,
		Score:   score,
	}
}

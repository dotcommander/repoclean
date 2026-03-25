package cleanup

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Rules holds configurable pattern lists for categorization.
// Zero values mean "use built-in defaults".
type Rules struct {
	DevArtifactPrefixes []string `json:"dev_artifact_prefixes,omitempty"`
	DevArtifactSuffixes []string `json:"dev_artifact_suffixes,omitempty"`
	AllowedRootMD       []string `json:"allowed_root_md,omitempty"`
	IgnoredDevDocSuffixes []string `json:"ignored_dev_doc_suffixes,omitempty"`
	IgnoredDeletePrefixes []string `json:"ignored_delete_prefixes,omitempty"`
	IgnoredSafeDirs     []string `json:"ignored_safe_dirs,omitempty"`
	ScaffoldFiles       []string `json:"scaffold_files,omitempty"`
	UntrackDirs         []string `json:"untrack_dirs,omitempty"`
}

// DefaultRules returns the built-in defaults (matching the current hardcoded values).
func DefaultRules() Rules {
	return Rules{
		DevArtifactPrefixes: []string{},
		DevArtifactSuffixes: []string{"_SUMMARY.md", "-spec.md", "_spec.md", "-state.json"},
		AllowedRootMD:       []string{"README.md", "CHANGELOG.md", "LICENSE.md", "CONTRIBUTING.md", "SECURITY.md", "CODE_OF_CONDUCT.md", "AGENTS.md"},
		IgnoredDevDocSuffixes: []string{"_GUIDE.md", "_REPORT.md", "_IMPLEMENTATION.md", "_ANALYSIS.md", "_PLAN.md", "_SUMMARY.md", "_PROGRESS.md", "_RESULTS.md"},
		IgnoredDeletePrefixes: []string{"verify-"},
		IgnoredSafeDirs:     []string{"data/", "bin/", "cache/", ".work/"},
		ScaffoldFiles:       []string{"public/vite.svg", "public/favicon.ico", "src/logo.svg", "src/App.css"},
		UntrackDirs:         []string{"dist/", "build/", "out/", "target/"},
	}
}

// LoadRules reads ~/.config/repoclean/rules.json and merges with defaults.
// If the file doesn't exist, returns defaults. Only non-empty fields override.
func LoadRules() Rules {
	defaults := DefaultRules()

	configDir, err := os.UserConfigDir()
	if err != nil {
		return defaults
	}
	path := filepath.Join(configDir, "repoclean", "rules.json")

	data, err := os.ReadFile(path)
	if err != nil {
		return defaults
	}

	var user Rules
	if err := json.Unmarshal(data, &user); err != nil {
		return defaults
	}

	// Merge: user fields override defaults only when non-nil/non-empty.
	if len(user.DevArtifactPrefixes) > 0 {
		defaults.DevArtifactPrefixes = user.DevArtifactPrefixes
	}
	if len(user.DevArtifactSuffixes) > 0 {
		defaults.DevArtifactSuffixes = user.DevArtifactSuffixes
	}
	if len(user.AllowedRootMD) > 0 {
		defaults.AllowedRootMD = user.AllowedRootMD
	}
	if len(user.IgnoredDevDocSuffixes) > 0 {
		defaults.IgnoredDevDocSuffixes = user.IgnoredDevDocSuffixes
	}
	if len(user.IgnoredDeletePrefixes) > 0 {
		defaults.IgnoredDeletePrefixes = user.IgnoredDeletePrefixes
	}
	if len(user.IgnoredSafeDirs) > 0 {
		defaults.IgnoredSafeDirs = user.IgnoredSafeDirs
	}
	if len(user.ScaffoldFiles) > 0 {
		defaults.ScaffoldFiles = user.ScaffoldFiles
	}
	if len(user.UntrackDirs) > 0 {
		defaults.UntrackDirs = user.UntrackDirs
	}

	return defaults
}

// IsZero reports whether r has no user-configured values.
func (r Rules) IsZero() bool {
	return len(r.DevArtifactPrefixes) == 0 &&
		len(r.DevArtifactSuffixes) == 0 &&
		len(r.AllowedRootMD) == 0 &&
		len(r.IgnoredDevDocSuffixes) == 0 &&
		len(r.IgnoredDeletePrefixes) == 0 &&
		len(r.IgnoredSafeDirs) == 0 &&
		len(r.ScaffoldFiles) == 0 &&
		len(r.UntrackDirs) == 0
}

// toSet converts a string slice to a map for O(1) lookups.
func toSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, item := range items {
		m[item] = true
	}
	return m
}

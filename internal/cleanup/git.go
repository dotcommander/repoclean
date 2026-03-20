package cleanup

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Enrich adds git intelligence to FileInfo entries: staleness and orphan detection.
func Enrich(files []FileInfo, cfg Config) error {
	recentSet, err := recentlyModified(cfg)
	if err != nil {
		log.Printf("cleanup: git staleness check skipped: %v", err)
		recentSet = nil
	}

	deletedSet, err := recentlyDeleted(cfg)
	if err != nil {
		log.Printf("cleanup: git orphan check skipped: %v", err)
		deletedSet = nil
	}

	// Collect stale candidates for per-file timestamp lookup.
	var stalePaths []string
	for i := range files {
		f := &files[i]
		if recentSet != nil && f.Tracked && !recentSet[f.RelPath] {
			stalePaths = append(stalePaths, f.RelPath)
		}
	}

	// Cap at 50 to bound subprocess cost.
	if len(stalePaths) > 50 {
		stalePaths = stalePaths[:50]
	}

	staleDaysMap := lastModifiedDays(cfg, stalePaths)

	for i := range files {
		f := &files[i]
		if recentSet != nil && f.Tracked && !recentSet[f.RelPath] {
			if days, ok := staleDaysMap[f.RelPath]; ok {
				f.StaleDays = days
			} else {
				f.StaleDays = cfg.StaleDays
			}
		}
		if deletedSet != nil && !f.Tracked && deletedSet[f.RelPath] {
			f.Orphaned = true
		}
	}

	return nil
}

// lastModifiedDays returns a map of relPath → actual stale days for each path.
// It runs git log -1 per file with a 2s timeout. Falls back to cfg.StaleDays on error.
func lastModifiedDays(cfg Config, relPaths []string) map[string]int {
	now := time.Now()
	result := make(map[string]int, len(relPaths))
	for _, p := range relPaths {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		cmd := exec.CommandContext(ctx, "git", "log", "-1", "--all", "--format=%ct", "--", p)
		cmd.Dir = cfg.Path
		out, err := cmd.Output()
		cancel()
		if err != nil {
			result[p] = cfg.StaleDays
			continue
		}
		ts := strings.TrimSpace(string(out))
		if ts == "" {
			result[p] = cfg.StaleDays
			continue
		}
		unix, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			result[p] = cfg.StaleDays
			continue
		}
		days := int(now.Sub(time.Unix(unix, 0)).Hours() / 24)
		if days < cfg.StaleDays {
			days = cfg.StaleDays // file is stale by definition (not in recentSet)
		}
		result[p] = days
	}
	return result
}

// gitLogToSet runs a git-log command and collects non-empty output lines into a set.
func gitLogToSet(cfg Config, label string, args ...string) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = cfg.Path

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log %s: %w", label, err)
	}

	set := make(map[string]bool)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			set[line] = true
		}
	}
	return set, scanner.Err()
}

// recentlyModified returns the set of file paths modified within staleDays.
func recentlyModified(cfg Config) (map[string]bool, error) {
	since := fmt.Sprintf("--since=%dd", cfg.StaleDays)
	return gitLogToSet(cfg, "recent", "git", "log", "--all", "--name-only", "--pretty=format:", since)
}

// recentlyDeleted returns the set of file paths deleted in the last 200 commits.
func recentlyDeleted(cfg Config) (map[string]bool, error) {
	return gitLogToSet(cfg, "deleted", "git", "log", "--diff-filter=D", "--name-only", "--pretty=format:", "-n", "200")
}

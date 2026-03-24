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
	stalenessMap, err := allFileStaleness(cfg)
	if err != nil {
		log.Printf("cleanup: git bulk staleness skipped: %v", err)
		stalenessMap = nil
	}

	deletedSet, err := recentlyDeleted(cfg)
	if err != nil {
		log.Printf("cleanup: git orphan check skipped: %v", err)
		deletedSet = nil
	}

	for i := range files {
		f := &files[i]
		if stalenessMap != nil && f.Tracked {
			if days, ok := stalenessMap[f.RelPath]; ok && days >= cfg.StaleDays {
				f.StaleDays = days
			}
		}
		if deletedSet != nil && !f.Tracked && deletedSet[f.RelPath] {
			f.Orphaned = true
		}
	}

	return nil
}

// allFileStaleness runs a single git log command and returns a map of
// relPath → days since last commit touching that file. First occurrence wins
// since git log outputs newest commits first.
func allFileStaleness(cfg Config) (map[string]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "log", "--all", "--name-only", "--pretty=format:%ct")
	cmd.Dir = cfg.Path

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log staleness: %w", err)
	}

	now := time.Now()
	result := make(map[string]int)
	var currentTS int64

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		// Numeric-only line is a unix timestamp.
		ts, err := strconv.ParseInt(line, 10, 64)
		if err == nil {
			currentTS = ts
			continue
		}
		// Non-numeric non-empty line is a file path touched by the current commit.
		if currentTS == 0 {
			continue
		}
		if _, seen := result[line]; seen {
			// First occurrence (most recent commit) already recorded — skip.
			continue
		}
		days := int(now.Sub(time.Unix(currentTS, 0)).Hours() / 24)
		result[line] = days
	}

	return result, scanner.Err()
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

// recentlyDeleted returns the set of file paths deleted in the last 200 commits.
func recentlyDeleted(cfg Config) (map[string]bool, error) {
	return gitLogToSet(cfg, "deleted", "git", "log", "--diff-filter=D", "--name-only", "--pretty=format:", "-n", "200")
}

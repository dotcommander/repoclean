package cleanup

import (
	"log"
	"path/filepath"
	"time"
)

// Enrich adds Git history intelligence to FileInfo entries.
func Enrich(files []FileInfo, cfg Config) error {
	repo, err := OpenRepository(cfg.Path)
	if err != nil {
		log.Printf("repoclean: repository history skipped: %v", err)
		return nil
	}
	return EnrichRepository(files, cfg, repo)
}

// EnrichRepository enriches files with an already-opened repository view.
// Read failures are warnings and never fabricate cleanup evidence.
func EnrichRepository(files []FileInfo, cfg Config, repo *Repository) error {
	if repo == nil {
		return nil
	}
	staleness, deleted, err := repo.history(time.Now())
	if err != nil {
		log.Printf("repoclean: repository history skipped: %v", err)
		return nil
	}
	for i := range files {
		f := &files[i]
		repoPath, ok := repo.repoRelative(f.Path)
		if !ok {
			continue
		}
		repoPath = filepath.Clean(repoPath)
		if f.Tracked {
			if days, found := staleness[repoPath]; found && days >= cfg.StaleDays {
				f.StaleDays = days
			}
		}
		if !f.GitStateUnknown && !f.Tracked && deleted[repoPath] {
			f.Orphaned = true
		}
	}
	return nil
}

func allFileStaleness(cfg Config) (map[string]int, error) {
	repo, err := OpenRepository(cfg.Path)
	if err != nil || repo == nil {
		return nil, err
	}
	stale, _, err := repo.history(time.Now())
	return stale, err
}

func recentlyDeleted(cfg Config) (map[string]bool, error) {
	repo, err := OpenRepository(cfg.Path)
	if err != nil || repo == nil {
		return nil, err
	}
	_, deleted, err := repo.history(time.Now())
	return deleted, err
}

package cleanup

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"github.com/go-git/go-git/v5/plumbing/object"
	filesystemstorage "github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/utils/merkletrie"
)

// Repository is the read-only Git view shared by walking and enrichment.
// Root and Prefix use filesystem paths; paths read from Git remain slash-separated.
type Repository struct {
	repo   *git.Repository
	Root   string
	Prefix string
}

// OpenRepository discovers the repository containing scanRoot. A nil repository
// and nil error means scanRoot is not inside a Git repository.
func OpenRepository(scanRoot string) (*Repository, error) {
	abs, err := filepath.Abs(scanRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve scan root: %w", err)
	}
	repo, err := git.PlainOpenWithOptions(abs, &git.PlainOpenOptions{DetectDotGit: true, EnableDotGitCommonDir: true})
	if errors.Is(err, git.ErrRepositoryNotExists) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open repository: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("open repository worktree: %w", err)
	}
	root := filepath.Clean(wt.Filesystem.Root())
	prefix, err := filepath.Rel(root, abs)
	if err != nil {
		return nil, fmt.Errorf("resolve repository scan prefix: %w", err)
	}
	return &Repository{repo: repo, Root: root, Prefix: prefix}, nil
}

// NewRepositoryView constructs a repository view for alternate storage-backed
// repositories in tests and integrations.
func NewRepositoryView(repo *git.Repository, root, scanRoot string) (*Repository, error) {
	prefix, err := filepath.Rel(filepath.Clean(root), filepath.Clean(scanRoot))
	if err != nil {
		return nil, fmt.Errorf("resolve repository scan prefix: %w", err)
	}
	return &Repository{repo: repo, Root: filepath.Clean(root), Prefix: prefix}, nil
}

func (r *Repository) markTracked(files []FileInfo) error {
	idx, err := r.repo.Storer.Index()
	if err != nil {
		return fmt.Errorf("read repository index: %w", err)
	}
	tracked := make(map[string]bool, len(idx.Entries))
	for _, entry := range idx.Entries {
		tracked[filepath.Clean(filepath.FromSlash(entry.Name))] = true
	}

	wt, err := r.repo.Worktree()
	if err != nil {
		return fmt.Errorf("open repository worktree: %w", err)
	}
	patterns, err := gitignore.ReadPatterns(wt.Filesystem, nil)
	if err != nil {
		return fmt.Errorf("read repository ignore patterns: %w", err)
	}
	patterns = append(patterns, repositoryExcludePatterns(r.repo)...)
	matcher := gitignore.NewMatcher(patterns)
	for i := range files {
		if files[i].IsDir {
			continue
		}
		repoPath, ok := r.repoRelative(files[i].Path)
		if !ok {
			continue
		}
		files[i].Tracked = tracked[repoPath]
		if !files[i].Tracked {
			files[i].Ignored = matcher.Match(strings.Split(filepath.ToSlash(repoPath), "/"), false)
		}
	}
	return nil
}

func repositoryExcludePatterns(repo *git.Repository) []gitignore.Pattern {
	storage, ok := repo.Storer.(*filesystemstorage.Storage)
	if !ok {
		return nil
	}
	f, err := storage.Filesystem().Open(filepath.Join("info", "exclude"))
	if err != nil {
		return nil
	}
	defer f.Close()

	var patterns []gitignore.Pattern
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, gitignore.ParsePattern(line, nil))
	}
	return patterns
}

func (r *Repository) repoRelative(path string) (string, bool) {
	rel, err := filepath.Rel(r.Root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return filepath.Clean(rel), true
}

type commitChange struct {
	when    time.Time
	touched []string
	deleted []string
}

func (r *Repository) changes() ([]commitChange, error) {
	refs, err := r.repo.References()
	if err != nil {
		return nil, fmt.Errorf("read references: %w", err)
	}
	defer refs.Close()

	seen := make(map[plumbing.Hash]bool)
	var commits []*object.Commit
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() != plumbing.HashReference {
			return nil
		}
		hash, err := r.repo.ResolveRevision(plumbing.Revision(ref.Name().String()))
		if errors.Is(err, plumbing.ErrReferenceNotFound) || errors.Is(err, plumbing.ErrObjectNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		iter, err := r.repo.Log(&git.LogOptions{From: *hash})
		if errors.Is(err, plumbing.ErrObjectNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		defer iter.Close()
		return iter.ForEach(func(c *object.Commit) error {
			if !seen[c.Hash] {
				seen[c.Hash] = true
				commits = append(commits, c)
			}
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("traverse references: %w", err)
	}
	sort.Slice(commits, func(i, j int) bool { return commits[i].Committer.When.After(commits[j].Committer.When) })

	changes := make([]commitChange, 0, len(commits))
	for _, commit := range commits {
		change, err := inspectCommit(commit)
		if err != nil {
			return nil, fmt.Errorf("inspect commit %s: %w", commit.Hash, err)
		}
		changes = append(changes, change)
	}
	return changes, nil
}

func inspectCommit(commit *object.Commit) (commitChange, error) {
	result := commitChange{when: commit.Committer.When}
	tree, err := commit.Tree()
	if err != nil {
		return result, err
	}
	if commit.NumParents() == 0 {
		err = tree.Files().ForEach(func(file *object.File) error {
			result.touched = append(result.touched, file.Name)
			return nil
		})
		return result, err
	}
	parent, err := commit.Parent(0)
	if err != nil {
		return result, err
	}
	parentTree, err := parent.Tree()
	if err != nil {
		return result, err
	}
	diff, err := object.DiffTree(parentTree, tree)
	if err != nil {
		return result, err
	}
	for _, change := range diff {
		action, err := change.Action()
		if err != nil {
			return result, err
		}
		switch action {
		case merkletrie.Delete:
			result.deleted = append(result.deleted, change.From.Name)
			result.touched = append(result.touched, change.From.Name)
		default:
			result.touched = append(result.touched, change.To.Name)
		}
	}
	return result, nil
}

func (r *Repository) history(now time.Time) (map[string]int, map[string]bool, error) {
	changes, err := r.changes()
	if err != nil {
		return nil, nil, err
	}
	stale := make(map[string]int)
	deleted := make(map[string]bool)
	for i, change := range changes {
		for _, path := range change.touched {
			path = filepath.Clean(filepath.FromSlash(path))
			if _, exists := stale[path]; !exists {
				stale[path] = int(now.Sub(change.when).Hours() / 24)
			}
		}
		if i < 200 {
			for _, path := range change.deleted {
				deleted[filepath.Clean(filepath.FromSlash(path))] = true
			}
		}
	}
	return stale, deleted, nil
}

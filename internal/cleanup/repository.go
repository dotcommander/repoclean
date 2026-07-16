package cleanup

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"github.com/go-git/go-git/v5/plumbing/object"
	filesystemstorage "github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/utils/merkletrie"
)

// Repository is the read-only Git view shared by walking and enrichment.
// Root and Prefix use filesystem paths; paths read from Git remain slash-separated.
type Repository struct {
	repo         *git.Repository
	Root         string
	Prefix       string
	resolvedRoot string
}

// OpenRepository discovers the repository containing scanRoot. A nil repository
// and nil error means scanRoot is not inside a Git repository.
func OpenRepository(scanRoot string) (*Repository, error) {
	abs, err := filepath.Abs(scanRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve scan root: %w", err)
	}
	resolvedAbs := abs
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		resolvedAbs = resolved
	}
	repo, err := git.PlainOpenWithOptions(resolvedAbs, &git.PlainOpenOptions{DetectDotGit: true, EnableDotGitCommonDir: true})
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
	resolvedRoot := filepath.Clean(wt.Filesystem.Root())
	prefix, err := filepath.Rel(resolvedRoot, resolvedAbs)
	if err != nil {
		return nil, fmt.Errorf("resolve repository scan prefix: %w", err)
	}
	if prefix == ".." || strings.HasPrefix(prefix, ".."+string(os.PathSeparator)) {
		return nil, fmt.Errorf("scan root %q is outside repository root %q", scanRoot, resolvedRoot)
	}
	root := abs
	if prefix != "." {
		for range strings.Split(filepath.ToSlash(prefix), "/") {
			root = filepath.Dir(root)
		}
	}
	return &Repository{repo: repo, Root: root, Prefix: prefix, resolvedRoot: resolvedRoot}, nil
}

// NewRepositoryView constructs a repository view for alternate storage-backed
// repositories in tests and integrations.
func NewRepositoryView(repo *git.Repository, root, scanRoot string) (*Repository, error) {
	resolvedScanRoot := filepath.Clean(scanRoot)
	if resolved, err := filepath.EvalSymlinks(resolvedScanRoot); err == nil {
		resolvedScanRoot = resolved
	}
	prefix, err := filepath.Rel(filepath.Clean(root), resolvedScanRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve repository scan prefix: %w", err)
	}
	return &Repository{
		repo:         repo,
		Root:         filepath.Clean(root),
		Prefix:       prefix,
		resolvedRoot: filepath.Clean(root),
	}, nil
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
	patterns, err := r.configurationExcludePatterns()
	if err != nil {
		return fmt.Errorf("read configured ignore patterns: %w", err)
	}
	patterns = append(patterns, repositoryExcludePatterns(r.repo)...)
	worktreePatterns, err := gitignore.ReadPatterns(wt.Filesystem, nil)
	if err != nil {
		return fmt.Errorf("read repository ignore patterns: %w", err)
	}
	patterns = append(patterns, worktreePatterns...)
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

func (r *Repository) configurationExcludePatterns() ([]gitignore.Pattern, error) {
	local, err := r.repo.Config()
	if err != nil {
		return nil, fmt.Errorf("read local Git config: %w", err)
	}
	if len(local.Raw.Includes) > 0 {
		return nil, fmt.Errorf("local Git config contains include directives")
	}
	if path := local.Raw.Section("core").Option("excludesfile"); path != "" {
		return readExcludePatterns(resolveExcludePath(r.resolvedRoot, path))
	}

	globalPath, err := scopeExcludePath(gitconfig.GlobalScope)
	if err != nil {
		return nil, fmt.Errorf("read global Git config: %w", err)
	}
	if globalPath != "" {
		return readExcludePatterns(resolveExcludePath(r.resolvedRoot, globalPath))
	}
	systemPath, err := scopeExcludePath(gitconfig.SystemScope)
	if err != nil {
		return nil, fmt.Errorf("read system Git config: %w", err)
	}
	if systemPath != "" {
		return readExcludePatterns(resolveExcludePath(r.resolvedRoot, systemPath))
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	return readExcludePatterns(filepath.Join(configHome, "git", "ignore"))
}

func scopeExcludePath(scope gitconfig.Scope) (string, error) {
	paths, err := gitConfigPaths(scope)
	if err != nil {
		return "", err
	}
	var selected string
	for _, path := range paths {
		excludePath, found, readErr := readCoreExcludeOption(path)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return "", readErr
		}
		if found {
			selected = excludePath
		}
	}
	return selected, nil
}

func gitConfigPaths(scope gitconfig.Scope) ([]string, error) {
	switch scope {
	case gitconfig.SystemScope:
		if os.Getenv("GIT_CONFIG_NOSYSTEM") != "" {
			return nil, nil
		}
		if path := os.Getenv("GIT_CONFIG_SYSTEM"); path != "" {
			return []string{path}, nil
		}
		return []string{"/etc/gitconfig"}, nil
	case gitconfig.GlobalScope:
		if path := os.Getenv("GIT_CONFIG_GLOBAL"); path != "" {
			return []string{path}, nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		configHome := os.Getenv("XDG_CONFIG_HOME")
		if configHome == "" {
			configHome = filepath.Join(home, ".config")
		}
		return []string{filepath.Join(configHome, "git", "config"), filepath.Join(home, ".gitconfig")}, nil
	default:
		return nil, fmt.Errorf("unsupported Git config scope %d", scope)
	}
}

func readCoreExcludeOption(path string) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	inCore := false
	var value string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			end := strings.IndexByte(line, ']')
			section := ""
			if end > 0 {
				section = strings.TrimSpace(line[1:end])
			}
			if strings.EqualFold(section, "include") || strings.HasPrefix(strings.ToLower(section), "includeif ") {
				return "", false, fmt.Errorf("Git config %q contains include directives", path)
			}
			inCore = strings.EqualFold(section, "core")
			continue
		}
		if !inCore {
			continue
		}
		key, raw, found := strings.Cut(line, "=")
		if !found || !strings.EqualFold(strings.TrimSpace(key), "excludesfile") {
			continue
		}
		value = strings.TrimSpace(raw)
		if unquoted, unquoteErr := strconv.Unquote(value); unquoteErr == nil {
			value = unquoted
		}
	}
	if err := scanner.Err(); err != nil {
		return "", false, err
	}
	return value, value != "", nil
}

func resolveExcludePath(root, path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(root, path)
}

func readExcludePatterns(path string) ([]gitignore.Pattern, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
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
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return patterns, nil
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
	rel, err := filepath.Rel(r.resolvedRoot, path)
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

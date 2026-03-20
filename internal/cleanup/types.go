package cleanup

import "time"

// Config holds CLI flags.
type Config struct {
	Path      string
	MaxDepth  int
	StaleDays int
}

// Category constants — backward compatible with bash script JSON keys.
const (
	CatDelete      = "delete_candidates"
	CatDevArtifact = "dev_artifact_candidates"
	CatArchive     = "archive_candidates"
	CatBrokenLink  = "broken_links"
	CatLargeFile   = "large_files"
	CatMisplaced    = "misplaced_scripts"
	CatMisplacedDoc = "misplaced_docs"
	CatUntrack      = "untrack_candidates"
	CatRenameDocs   = "rename_docs"
)

// ContentClass for content-aware classification.
type ContentClass int

const (
	ContentUnknown ContentClass = iota
	ContentGenerated
	ContentScratch
	ContentTodoOnly
	ContentLogDump
	ContentConfig
	ContentMeaningful
)

// String returns the lowercase name for a ContentClass.
func (c ContentClass) String() string {
	switch c {
	case ContentGenerated:
		return "generated"
	case ContentScratch:
		return "scratch"
	case ContentTodoOnly:
		return "todo_list"
	case ContentLogDump:
		return "log_dump"
	case ContentConfig:
		return "config"
	case ContentMeaningful:
		return "meaningful"
	default:
		return "unknown"
	}
}

// Status labels for file inventory.
const (
	StatusClean           = "clean"
	StatusDelete          = "delete"
	StatusArchive         = "archive"
	StatusUntrack         = "untrack"
	StatusDevArtifact     = "dev_artifact"
	StatusMisplacedDoc    = "misplaced_doc"
	StatusMisplacedScript = "misplaced_script"
	StatusLargeFile       = "large_file"
	StatusBrokenLink      = "broken_link"
	StatusRenameDoc       = "rename_doc"
)

// FileInfo is the internal enriched representation per file.
type FileInfo struct {
	Path       string       // absolute
	RelPath    string       // relative to Config.Path
	Size       int64
	IsDir      bool
	IsSymlink  bool
	IsEmpty    bool         // empty directory
	Tracked    bool
	Ignored    bool         // matched by .gitignore
	StaleDays  int          // 0 = recently modified
	ModTime    time.Time
	Content    ContentClass
	LinkTarget string       // for symlinks, readlink value
	Duplicate  string       // if set, path of the original this duplicates
	Executable bool         // has executable permission bit
	Orphaned   bool         // deleted from git history but still exists
}

// FileCandidate is the enriched JSON output per file (superset of bash fields).
type FileCandidate struct {
	File        string `json:"file"`
	Reason      string `json:"reason,omitempty"`
	SizeKB      int64  `json:"size_kb"`
	Tracked     *bool  `json:"tracked,omitempty"`
	Referenced  *bool  `json:"referenced,omitempty"`
	Target      string `json:"target,omitempty"`
	StaleDays   int    `json:"stale_days,omitempty"`
	Score       int    `json:"score,omitempty"`
	ContentHint string `json:"content_hint,omitempty"`
}

// LabeledFile tags every walked file with a disposition status.
type LabeledFile struct {
	File   string `json:"file"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	SizeKB int64  `json:"size_kb"`
}

// ScanResult is the top-level JSON output — backward compatible.
type ScanResult struct {
	Path                  string          `json:"path"`
	DeleteCandidates      []FileCandidate `json:"delete_candidates"`
	DevArtifactCandidates []FileCandidate `json:"dev_artifact_candidates"`
	ArchiveCandidates     []FileCandidate `json:"archive_candidates"`
	BrokenLinks           []FileCandidate `json:"broken_links"`
	LargeFiles            []FileCandidate `json:"large_files"`
	MisplacedScripts      []FileCandidate `json:"misplaced_scripts"`
	MisplacedDocs         []FileCandidate `json:"misplaced_docs"`
	UntrackCandidates     []FileCandidate `json:"untrack_candidates"`
	RenameDocs            []FileCandidate `json:"rename_docs,omitempty"`
	AllFiles              []LabeledFile   `json:"all_files,omitempty"`
	Summary               map[string]int  `json:"summary"`
}

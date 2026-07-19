package cleanup

import "fmt"

// EmitFindings converts enriched FileInfo fields into normalized findings.
// Call after WalkRepository, EnrichRepository, Classify, and FindDuplicates.
func EmitFindings(files []FileInfo) {
	for i := range files {
		f := &files[i]

		if !f.GitStateUnknown && !f.Tracked {
			f.AddFinding(Finding{Source: "walker", Rule: RuleUntracked, Severity: SevInfo, Confidence: 1.0, Message: "not tracked by git"})
		}
		if f.IsEmpty {
			f.AddFinding(Finding{Source: "walker", Rule: RuleEmpty, Severity: SevInfo, Confidence: 1.0, Message: "empty directory"})
		}

		switch {
		case f.StaleDays > 365:
			f.AddFinding(Finding{Source: "git", Rule: RuleStale, Severity: SevError, Confidence: 1.0, Message: fmt.Sprintf("stale %d days", f.StaleDays)})
		case f.StaleDays > 180:
			f.AddFinding(Finding{Source: "git", Rule: RuleStale, Severity: SevWarn, Confidence: 0.8, Message: fmt.Sprintf("stale %d days", f.StaleDays)})
		case f.StaleDays > 90:
			f.AddFinding(Finding{Source: "git", Rule: RuleStale, Severity: SevInfo, Confidence: 0.6, Message: fmt.Sprintf("stale %d days", f.StaleDays)})
		}

		switch {
		case f.Size > 100*1024*1024:
			f.AddFinding(Finding{Source: "walker", Rule: RuleLargeFile, Severity: SevError, Confidence: 1.0, Message: fmt.Sprintf("file size %dMB", f.Size/1024/1024)})
		case f.Size > 10*1024*1024:
			f.AddFinding(Finding{Source: "walker", Rule: RuleLargeFile, Severity: SevWarn, Confidence: 1.0, Message: fmt.Sprintf("file size %dMB", f.Size/1024/1024)})
		}

		switch f.Content {
		case ContentGenerated:
			f.AddFinding(Finding{Source: "classify", Rule: RuleGenerated, Severity: SevInfo, Confidence: 0.8, Message: "generated content detected"})
		case ContentScratch:
			f.AddFinding(Finding{Source: "classify", Rule: RuleScratch, Severity: SevInfo, Confidence: 0.7, Message: "scratch/notes content"})
		case ContentTodoOnly:
			f.AddFinding(Finding{Source: "classify", Rule: RuleTodoOnly, Severity: SevInfo, Confidence: 0.7, Message: "todo-only content"})
		case ContentLogDump:
			f.AddFinding(Finding{Source: "classify", Rule: RuleLogDump, Severity: SevInfo, Confidence: 0.7, Message: "log dump content"})
		}

		if f.Orphaned {
			f.AddFinding(Finding{Source: "git", Rule: RuleOrphaned, Severity: SevWarn, Confidence: 0.9, Message: "deleted from git history"})
		}
		if f.Duplicate != "" {
			f.AddFinding(Finding{Source: "duplicates", Rule: RuleDuplicate, Severity: SevWarn, Confidence: 0.8, Message: "duplicate of " + f.Duplicate})
		}
	}
}

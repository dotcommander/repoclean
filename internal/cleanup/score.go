package cleanup

// findingWeights maps rule → [info, warn, error] score contribution.
var findingWeights = map[string][3]int{
	RuleUntracked: {20, 20, 20},
	RuleStale:     {15, 25, 35},
	RuleLargeFile: {0, 10, 20},
	RuleGenerated: {15, 15, 15},
	RuleScratch:   {20, 20, 20},
	RuleTodoOnly:  {20, 20, 20},
	RuleLogDump:   {10, 10, 10},
	RuleOrphaned:  {25, 25, 25},
	RuleDuplicate: {20, 20, 20},
	RuleEmpty:     {30, 30, 30},
}

// CalculateHealth returns a 0-100 score for the repository.
// Higher is better. 100 = zero cleanup candidates.
func CalculateHealth(result ScanResult) int {
	totalFiles := 0
	cleanFiles := 0
	for _, f := range result.AllFiles {
		totalFiles++
		if f.Status == StatusClean {
			cleanFiles++
		}
	}

	if totalFiles == 0 {
		return 100
	}

	// 1. Ratio Deduction (up to 40 pts)
	// If 50% of files are "messy", score starts at 60.
	health := (cleanFiles * 100) / totalFiles
	health = (health * 40) / 100 + 60

	// 2. Severity Penalties (up to 40 pts)
	// Critical items like broken links, large tracked binaries, or old stale files.
	deductions := 0
	deductions += len(result.BrokenLinks) * 5
	deductions += len(result.LargeFiles) * 3
	deductions += len(result.UntrackCandidates) * 2
	deductions += len(result.DeleteCandidates) * 1

	if deductions > 40 {
		deductions = 40
	}
	health -= deductions

	// 3. Size Penalty (up to 20 pts)
	// Massive amounts of data or artifacts.
	var junkKB int64
	for _, c := range result.DeleteCandidates {
		junkKB += c.SizeKB
	}
	for _, c := range result.ArchiveCandidates {
		junkKB += c.SizeKB
	}
	// >1GB of junk is -20 pts.
	sizePenalty := int(junkKB / (50 * 1024)) // -1 pt per 50MB
	if sizePenalty > 20 {
		sizePenalty = 20
	}
	health -= sizePenalty

	if health < 0 {
		return 0
	}
	return health
}

// Score returns a 0-100 cleanup confidence score based on findings.
func Score(f *FileInfo) int {
	score := 0
	for _, finding := range f.Findings {
		w, ok := findingWeights[finding.Rule]
		if !ok {
			continue
		}
		sev := finding.Severity
		if sev > 2 {
			sev = 2
		}
		score += w[sev]
	}
	if score > 100 {
		return 100
	}
	return score
}

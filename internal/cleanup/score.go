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

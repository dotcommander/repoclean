package cleanup

// Score returns a 0-100 cleanup confidence score for a file.
func Score(f *FileInfo) int {
	score := 0

	if !f.Tracked {
		score += 20
	}

	switch {
	case f.StaleDays > 365:
		score += 35
	case f.StaleDays > 180:
		score += 25
	case f.StaleDays > 90:
		score += 15
	}

	switch {
	case f.Size > 100*1024*1024:
		score += 20
	case f.Size > 10*1024*1024:
		score += 10
	}

	switch f.Content {
	case ContentGenerated:
		score += 15
	case ContentScratch, ContentTodoOnly:
		score += 20
	case ContentLogDump:
		score += 10
	}

	if f.Orphaned {
		score += 25
	}
	if f.Duplicate != "" {
		score += 20
	}
	if f.IsEmpty {
		score += 30
	}

	if score > 100 {
		return 100
	}
	return score
}

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/dotcommander/repoclean/internal/cleanup"
)

func boolStr(b *bool) string {
	if b == nil {
		return "unknown"
	}
	if *b {
		return "yes"
	}
	return "no"
}

type reportSection struct {
	Title  string // fmt format with one %d for count
	Format string
	Header []string
	Rows   [][]string
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func printSections(w *os.File, sections []reportSection) {
	for _, s := range sections {
		if len(s.Rows) == 0 {
			continue
		}
		fmt.Fprintf(w, s.Title+"\n", len(s.Rows))
		fmt.Fprintf(w, s.Format, toAny(s.Header)...)
		for _, row := range s.Rows {
			fmt.Fprintf(w, s.Format, toAny(row)...)
		}
		fmt.Fprintln(w)
	}
}

func printReport(result cleanup.ScanResult) {
	w := os.Stdout
	bar := strings.Repeat("\u2550", 30)
	fmt.Fprintf(w, "Cleanup Plan \u2014 %s\n%s\n\n", result.Path, bar)

	sections := []reportSection{
		{Title: "DELETE (%d files)", Format: "  %-8s %-30s %-20s %s\n", Header: []string{"Action", "File", "Reason", "Size"}},
		{Title: "DEV ARTIFACTS (%d files \u2014 untrack + archive)", Format: "  %-8s %-30s %-20s %s\n", Header: []string{"Action", "File", "Reason", "Size"}},
		{Title: "ARCHIVE (%d files \u2014 move to .work/archive/)", Format: "  %-8s %-30s %-20s %6s  %s\n", Header: []string{"Action", "File", "Reason", "Score", "Hint"}},
		{Title: "LARGE FILES (%d file \u2014 manual review)", Format: "  %-8s %-30s %-10s %s\n", Header: []string{"Action", "File", "Size", "Tracked"}},
		{Title: "MISPLACED DOCS (%d files \u2014 move to docs/)", Format: "  %-8s %-30s %s\n", Header: []string{"Action", "File", "Reason"}},
		{Title: "RENAME DOCS (%d files \u2014 normalize filenames)", Format: "  %-8s %-40s %s\n", Header: []string{"Action", "File", "New Name"}},
		{Title: "MISPLACED SCRIPTS (%d file)", Format: "  %-8s %-30s %-10s %s\n", Header: []string{"Action", "File", "Tracked", "Referenced"}},
		{Title: "UNTRACK (%d files \u2014 should not be in git)", Format: "  %-8s %-30s %-25s %s\n", Header: []string{"Action", "File", "Reason", "Size"}},
		{Title: "BROKEN LINKS (%d)", Format: "  %-8s %-30s %s\n", Header: []string{"Action", "File", "Target"}},
	}

	for _, c := range result.DeleteCandidates {
		sections[0].Rows = append(sections[0].Rows, []string{"delete", c.File, c.Reason, fmtSize(c.SizeKB)})
	}
	for _, c := range result.DevArtifactCandidates {
		sections[1].Rows = append(sections[1].Rows, []string{"git rm", c.File, c.Reason, fmtSize(c.SizeKB)})
	}
	for _, c := range result.ArchiveCandidates {
		sections[2].Rows = append(sections[2].Rows, []string{"archive", c.File, c.Reason, fmt.Sprintf("%d", c.Score), c.ContentHint})
	}
	for _, c := range result.LargeFiles {
		sections[3].Rows = append(sections[3].Rows, []string{"review", c.File, fmtSize(c.SizeKB), boolStr(c.Tracked)})
	}
	for _, c := range result.MisplacedDocs {
		sections[4].Rows = append(sections[4].Rows, []string{"move \u2192", c.File + " \u2192 docs/" + c.File, c.Reason})
	}
	for _, c := range result.RenameDocs {
		sections[5].Rows = append(sections[5].Rows, []string{"rename", c.File, c.Target})
	}
	for _, c := range result.MisplacedScripts {
		sections[6].Rows = append(sections[6].Rows, []string{"move \u2192", c.File + " \u2192 scripts/", boolStr(c.Tracked), boolStr(c.Referenced)})
	}
	for _, c := range result.UntrackCandidates {
		sections[7].Rows = append(sections[7].Rows, []string{"untrack", c.File, c.Reason, fmtSize(c.SizeKB)})
	}
	for _, c := range result.BrokenLinks {
		sections[8].Rows = append(sections[8].Rows, []string{"unlink", c.File, c.Target})
	}

	printSections(w, sections)

	if len(result.AllFiles) > 0 {
		fmt.Fprintf(w, "ALL FILES (%d)\n", len(result.AllFiles))
		fmt.Fprintf(w, "  %-18s %-40s %s\n", "Status", "File", "Reason")
		for _, f := range result.AllFiles {
			reason := f.Reason
			if reason == "" {
				reason = "-"
			}
			fmt.Fprintf(w, "  %-18s %-40s %s\n", f.Status, f.File, reason)
		}
		fmt.Fprintln(w)
	}

	var parts []string
	if n := len(result.DeleteCandidates); n > 0 {
		parts = append(parts, fmt.Sprintf("%d delete", n))
	}
	if n := len(result.DevArtifactCandidates); n > 0 {
		parts = append(parts, fmt.Sprintf("%d untrack", n))
	}
	if n := len(result.ArchiveCandidates); n > 0 {
		parts = append(parts, fmt.Sprintf("%d archive", n))
	}
	if n := len(result.LargeFiles); n > 0 {
		parts = append(parts, fmt.Sprintf("%d review", n))
	}
	if n := len(result.MisplacedScripts); n > 0 {
		parts = append(parts, fmt.Sprintf("%d move scripts", n))
	}
	if n := len(result.MisplacedDocs); n > 0 {
		parts = append(parts, fmt.Sprintf("%d move docs", n))
	}
	if n := len(result.RenameDocs); n > 0 {
		parts = append(parts, fmt.Sprintf("%d rename docs", n))
	}
	if n := len(result.UntrackCandidates); n > 0 {
		parts = append(parts, fmt.Sprintf("%d untrack", n))
	}
	if n := len(result.BrokenLinks); n > 0 {
		parts = append(parts, fmt.Sprintf("%d broken links", n))
	}
	if len(parts) == 0 {
		fmt.Fprintln(w, "Summary: nothing to clean up")
	} else {
		fmt.Fprintf(w, "Summary: %s\n", strings.Join(parts, ", "))
	}
}

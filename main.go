package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/dotcommander/repoclean/internal/cleanup"
)

func main() {
	path := flag.String("path", ".", "directory to scan")
	maxDepth := flag.Int("max-depth", 5, "maximum directory depth")
	staleDays := flag.Int("stale-days", 90, "days before a file is considered stale")
	report := flag.Bool("report", false, "output human-readable cleanup plan instead of JSON")
	findings := flag.Bool("findings", false, "output findings-focused view grouped by signal")
	execMode := flag.Bool("exec", false, "output shell commands for automated execution")
	apply := flag.Bool("apply", false, "dry-run: show commands that would execute (add --confirm to run)")
	confirm := flag.Bool("confirm", false, "actually execute commands (requires --apply)")
	missing := flag.Bool("missing", false, "check repo completeness (missing LICENSE, CI, etc.)")
	flag.Parse()
	if flag.NArg() > 0 {
		log.Fatalf("repoclean: unexpected argument %q", flag.Arg(0))
	}

	absPath, err := filepath.Abs(*path)
	if err != nil {
		log.Fatalf("repoclean: resolve path: %v", err)
	}

	cfg := cleanup.Config{
		Path:      absPath,
		MaxDepth:  *maxDepth,
		StaleDays: *staleDays,
	}
	cfg.Rules = cleanup.LoadRules()

	start := time.Now()
	repo, repoErr := cleanup.OpenRepository(cfg.Path)
	if repoErr != nil {
		log.Printf("repoclean: repository metadata skipped: %v", repoErr)
	}
	files, err := cleanup.WalkRepository(cfg, repo)
	if err != nil {
		log.Fatalf("repoclean: walk: %v", err)
	}
	if repoErr != nil {
		cleanup.MarkRepositoryStateUnknown(files)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		log.Printf("repoclean: walk took %v (large repo?)", elapsed)
	}

	if *missing {
		cr := cleanup.CheckCompleteness(files)
		cr.Path = absPath
		printMissing(cr)
		return
	}

	if err := cleanup.EnrichRepository(files, cfg, repo); err != nil {
		log.Printf("repoclean: enrich warning: %v", err)
	}

	if err := cleanup.Classify(files); err != nil {
		log.Printf("repoclean: classify warning: %v", err)
	}

	cleanup.FindDuplicates(files)
	cleanup.EmitFindings(files)

	result := cleanup.Categorize(files, cfg)
	result.Path = absPath

	if *confirm && !*apply {
		log.Fatal("repoclean: --confirm requires --apply")
	}

	if *apply {
		cmds := buildCommands(result)
		if len(cmds) == 0 {
			fmt.Println("nothing to clean up")
			return
		}
		if *confirm {
			if err := runCommands(cmds, absPath); err != nil {
				log.Fatalf("repoclean: apply: %v", err)
			}
		} else {
			dryRun(cmds)
		}
		return
	}

	if *execMode {
		printExec(result)
		return
	}

	if *findings {
		printFindings(files)
		return
	}

	if *report {
		printReport(result)
		return
	}

	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		log.Fatalf("repoclean: marshal: %v", err)
	}
	fmt.Println(string(out))
}

func fmtSize(kb int64) string {
	b := float64(kb) * 1024
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", int64(b))
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", b/float64(div), "KMGTPE"[exp])
}

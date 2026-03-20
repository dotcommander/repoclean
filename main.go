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
	execMode := flag.Bool("exec", false, "output shell commands for automated execution")
	apply := flag.Bool("apply", false, "dry-run: show commands that would execute (add --confirm to run)")
	confirm := flag.Bool("confirm", false, "actually execute commands (requires --apply)")
	flag.Parse()

	absPath, err := filepath.Abs(*path)
	if err != nil {
		log.Fatalf("cleanup-scanner: resolve path: %v", err)
	}

	cfg := cleanup.Config{
		Path:      absPath,
		MaxDepth:  *maxDepth,
		StaleDays: *staleDays,
	}

	start := time.Now()
	files, err := cleanup.Walk(cfg)
	if err != nil {
		log.Fatalf("cleanup-scanner: walk: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		log.Printf("cleanup-scanner: walk took %v (large repo?)", time.Since(start))
	}

	if err := cleanup.Enrich(files, cfg); err != nil {
		log.Printf("cleanup-scanner: enrich warning: %v", err)
	}

	if err := cleanup.Classify(files); err != nil {
		log.Printf("cleanup-scanner: classify warning: %v", err)
	}

	cleanup.FindDuplicates(files)
	cleanup.EmitFindings(files)

	result := categorize(files, cfg)
	result.Path = absPath

	if *confirm && !*apply {
		log.Fatal("cleanup-scanner: --confirm requires --apply")
	}

	if *apply {
		cmds := buildCommands(result)
		if len(cmds) == 0 {
			fmt.Println("nothing to clean up")
			return
		}
		if *confirm {
			runCommands(cmds, absPath)
		} else {
			dryRun(cmds)
		}
		return
	}

	if *execMode {
		printExec(result)
		return
	}

	if *report {
		printReport(result)
		return
	}

	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		log.Fatalf("cleanup-scanner: marshal: %v", err)
	}
	fmt.Println(string(out))
}

func fmtSize(kb int64) string {
	if kb >= 1024 {
		return fmt.Sprintf("%4dMB", kb/1024)
	}
	return fmt.Sprintf("%4dKB", kb)
}

package server

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/storage"
	"github.com/cristianadrielbraun/gofer/internal/store"
)

// RunStorageCommand handles the offline "storage" commands. It reports whether
// args named one, and the exit code.
func RunStorageCommand(ctx context.Context, args []string, stdout, stderr io.Writer) (bool, int) {
	if len(args) == 0 || args[0] != "storage" {
		return false, 0
	}
	if len(args) > 1 && args[1] == "migrate" {
		return true, runStorageMigrationCommand(ctx, args[2:], stdout, stderr)
	}
	if len(args) > 1 && args[1] == "compress" {
		return true, runStorageCompressCommand(ctx, args[2:], stdout, stderr)
	}
	if len(args) < 2 || args[1] != "inspect" {
		fmt.Fprintln(stderr, "usage: gofer storage inspect [--db PATH] | migrate [--db PATH] --to PATH [--working-dir PATH] [--retry] | compress [--dir PATH] [--dry-run]")
		return true, 2
	}
	flags := flag.NewFlagSet("storage inspect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("db", ConfiguredDatabasePath(), "shared database to inspect")
	if err := flags.Parse(args[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return true, 0
		}
		return true, 2
	}
	if flags.NArg() != 0 || *path == "" {
		fmt.Fprintln(stderr, "usage: gofer storage inspect [--db PATH]")
		return true, 2
	}
	report, err := storage.InspectUserStorageMigration(ctx, *path)
	if err != nil {
		fmt.Fprintf(stderr, "storage inspection failed: %v\n", err)
		return true, 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintf(stderr, "write storage inspection: %v\n", err)
		return true, 1
	}
	return true, 0
}

func runStorageMigrationCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("storage migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	source := flags.String("db", ConfiguredDatabasePath(), "original shared database")
	destination := flags.String("to", "", "new central database in the original data directory")
	workingDirectory := flags.String("working-dir", "", "original Gofer working directory (defaults to current directory)")
	retry := flags.Bool("retry", false, "resume recognized interrupted migration work")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *source == "" || *destination == "" {
		fmt.Fprintln(stderr, "usage: gofer storage migrate [--db PATH] --to PATH [--working-dir PATH] [--retry]")
		return 2
	}
	key, err := loadExistingMigrationKey(*source)
	if err != nil {
		fmt.Fprintf(stderr, "storage migration failed: %v\n", err)
		return 1
	}
	if !*retry {
		if err := requireManagedSharedDatabase(ctx, *source); err != nil {
			fmt.Fprintf(stderr, "storage migration failed: %v\n", err)
			return 1
		}
	}
	options := migrationOptions(*source, *destination, key)
	options.WorkingDirectory = *workingDirectory
	options.Retry = *retry
	layout, err := storage.MigrateUserStorage(ctx, options)
	if err != nil {
		fmt.Fprintf(stderr, "storage migration failed: %v\n", err)
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(layout); err != nil {
		fmt.Fprintf(stderr, "write storage migration result: %v\n", err)
		return 1
	}
	return 0
}

// compressProgressInterval is how often storage compress reports progress.
var compressProgressInterval = 2 * time.Second

// runStorageCompressCommand compresses message files saved before Gofer
// compressed them. It can run while Gofer is running. Progress and a summary
// go to stderr; the JSON report goes to stdout.
func runStorageCompressCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("storage compress", flag.ContinueOnError)
	flags.SetOutput(stderr)
	directory := flags.String("dir", filepath.Join(filepath.Dir(ConfiguredDatabasePath()), "accounts"), "message file directory")
	dryRun := flags.Bool("dry-run", false, "report what would be saved without changing files")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *directory == "" {
		fmt.Fprintln(stderr, "usage: gofer storage compress [--dir PATH] [--dry-run]")
		return 2
	}
	verb := "Compressing"
	if *dryRun {
		verb = "Checking (dry run, nothing is changed)"
	}
	fmt.Fprintf(stderr, "%s message files in %s\n", verb, *directory)
	started := time.Now()
	lastReport := started
	report, err := store.CompressMessageFiles(ctx, *directory, *dryRun, func(progress store.CompressReport) {
		if now := time.Now(); now.Sub(lastReport) >= compressProgressInterval {
			lastReport = now
			fmt.Fprintf(stderr, "  %s\n", compressSummary(progress, now.Sub(started)))
		}
	})
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if encodeErr := encoder.Encode(report); encodeErr != nil {
		fmt.Fprintf(stderr, "write storage compression result: %v\n", encodeErr)
		return 1
	}
	elapsed := time.Since(started)
	if errors.Is(err, context.Canceled) {
		fmt.Fprintf(stderr, "Interrupted: %s\nFiles already compressed stay compressed; run the command again to continue.\n", compressSummary(report, elapsed))
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "Stopped: %v\n%s\n", err, compressSummary(report, elapsed))
		return 1
	}
	fmt.Fprintf(stderr, "Done: %s\n", compressSummary(report, elapsed))
	if report.AlreadyCompressed > 0 || report.LeftAsIs > 0 || report.ChangedMeanwhile > 0 {
		fmt.Fprintf(stderr, "  %s already compressed, %s left as they were (small or would not shrink), %s skipped because Gofer changed them meanwhile\n",
			formatCount(report.AlreadyCompressed), formatCount(report.LeftAsIs), formatCount(report.ChangedMeanwhile))
	}
	if report.Failed > 0 {
		fmt.Fprintf(stderr, "%s files could not be compressed and were left unchanged\n", formatCount(report.Failed))
		return 1
	}
	return 0
}

// compressSummary is one line of storage compress progress.
func compressSummary(report store.CompressReport, elapsed time.Duration) string {
	action := "compressed"
	if report.DryRun {
		action = "would compress"
	}
	return fmt.Sprintf("checked %s, %s %s, %s → %s, %s",
		filesCount(report.Scanned), action, formatCount(report.Compressed),
		formatBytes(report.BytesBefore), formatBytes(report.BytesAfter), elapsed.Round(time.Second))
}

func filesCount(n int) string {
	if n == 1 {
		return "1 file"
	}
	return formatCount(n) + " files"
}

func formatCount(n int) string {
	digits := strconv.Itoa(n)
	var out []byte
	for i := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, digits[i])
	}
	return string(out)
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

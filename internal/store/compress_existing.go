package store

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// messageFileNames are the files CompressMessageFiles converts: the raw
// message and its bodies. Attachments and images are left as they are.
var messageFileNames = map[string]bool{
	"raw.eml":            true,
	"body.txt":           true,
	"body.html":          true,
	"body_original.html": true,
	"body_remote.html":   true,
}

// compressTempPrefix names the temporary files a conversion writes next to the
// file it replaces; one left by an interrupted run is removed by the next.
const compressTempPrefix = ".compress-"

// CompressReport counts what CompressMessageFiles found and did.
type CompressReport struct {
	Directory         string `json:"directory"`
	DryRun            bool   `json:"dry_run"`
	Scanned           int    `json:"scanned"`
	Compressed        int    `json:"compressed"`
	AlreadyCompressed int    `json:"already_compressed"`
	LeftAsIs          int    `json:"left_as_is"`
	ChangedMeanwhile  int    `json:"changed_meanwhile"`
	Failed            int    `json:"failed"`
	BytesBefore       int64  `json:"bytes_before"`
	BytesAfter        int64  `json:"bytes_after"`
	LeftoversRemoved  int    `json:"leftovers_removed"`
}

// CompressMessageFiles compresses the raw messages and bodies under root that
// were saved before compression, as new ones are saved. It is safe while
// Gofer runs: each file is replaced by an atomic rename of a compressed copy
// written next to it, and a file that changes meanwhile is skipped. With
// dryRun it only counts. A file it cannot convert is counted as failed and
// left unchanged; only walking root itself can fail the run. progress, when
// set, receives the running totals after each message file.
func CompressMessageFiles(ctx context.Context, root string, dryRun bool, progress func(CompressReport)) (CompressReport, error) {
	report := CompressReport{Directory: root, DryRun: dryRun}
	info, err := os.Stat(root)
	if err != nil {
		return report, err
	}
	if !info.IsDir() {
		return report, errors.New("not a directory: " + root)
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if path == root {
				return walkErr
			}
			report.Failed++
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		name := entry.Name()
		if strings.HasPrefix(name, compressTempPrefix) {
			if !dryRun && os.Remove(path) == nil {
				report.LeftoversRemoved++
			}
			return nil
		}
		if !messageFileNames[name] {
			return nil
		}
		report.Scanned++
		compressOne(path, dryRun, &report)
		if progress != nil {
			progress(report)
		}
		return nil
	})
	return report, err
}

func compressOne(path string, dryRun bool, report *CompressReport) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		report.Failed++
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		report.Failed++
		return
	}
	if bytes.HasPrefix(data, zstdMagic) {
		report.AlreadyCompressed++
		return
	}
	encoded := encodeMessageFile(data)
	if len(encoded) == len(data) {
		report.LeftAsIs++
		return
	}
	if dryRun {
		report.Compressed++
		report.BytesBefore += int64(len(data))
		report.BytesAfter += int64(len(encoded))
		return
	}
	if err := replaceIfUnchanged(path, before, encoded); err != nil {
		if errors.Is(err, errChangedMeanwhile) {
			report.ChangedMeanwhile++
		} else {
			report.Failed++
		}
		return
	}
	report.Compressed++
	report.BytesBefore += int64(len(data))
	report.BytesAfter += int64(len(encoded))
}

var errChangedMeanwhile = errors.New("file changed during conversion")

// replaceIfUnchanged writes data to a temporary file beside path and renames
// it over path, unless path no longer matches before.
func replaceIfUnchanged(path string, before fs.FileInfo, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), compressTempPrefix+"*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmpPath, before.Mode().Perm()); err != nil {
		return err
	}
	now, err := os.Lstat(path)
	if err != nil || now.Size() != before.Size() || !now.ModTime().Equal(before.ModTime()) || !os.SameFile(now, before) {
		return errChangedMeanwhile
	}
	return os.Rename(tmpPath, path)
}

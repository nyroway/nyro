package parquet

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/parquet-go/parquet-go"
)

// ReadSince opens every completed parquet file in <dir>/<signal>/ whose hour
// bucket is >= since (since<=0 → all) and returns all rows. Files are visited
// in name order (oldest first). Only completed files (no .tmp suffix) are read.
func ReadSince[Row any](dir, signal string, since int64) ([]Row, error) {
	matches, err := filepath.Glob(filepath.Join(dir, signal, "*.parquet"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	var out []Row
	for _, name := range matches {
		if since > 0 {
			if h, ok := hourOf(filepath.Base(name)); ok && h < since {
				continue
			}
		}
		rows, err := readParquetFile[Row](name)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// hourOf parses the leading YYYYMMDDHH from a file base name into a unix-nano
// hour bucket; ok=false if the name does not match the convention.
func hourOf(base string) (int64, bool) {
	const layout = "2006010215"
	if len(base) < len(layout) {
		return 0, false
	}
	t, err := time.Parse(layout, base[:len(layout)])
	if err != nil {
		return 0, false
	}
	return t.UnixNano(), true
}

// CountFiles returns the number of completed parquet files for a signal
// (used by janitor tests and ClearAll).
func CountFiles(dir, signal string) (int, error) {
	matches, err := filepath.Glob(filepath.Join(dir, signal, "*.parquet"))
	return len(matches), err
}

// RemoveAll deletes every parquet file for a signal (ClearAll implementation).
func RemoveAll(dir, signal string) (int, error) {
	matches, err := filepath.Glob(filepath.Join(dir, signal, "*.parquet"))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range matches {
		if err := os.Remove(m); err == nil {
			n++
		}
	}
	return n, nil
}

func readParquetFile[Row any](name string) ([]Row, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	pr := parquet.NewGenericReader[Row](f)
	rows := make([]Row, pr.NumRows())
	// parquet-go's Read returns io.EOF when it has drained the file, even on a
	// full read into a buffer sized exactly to NumRows. Treat io.EOF as success;
	// any other error (or a short read that isn't EOF) propagates.
	n, err := pr.Read(rows)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if n < len(rows) {
		rows = rows[:n]
	}
	return rows, pr.Close()
}

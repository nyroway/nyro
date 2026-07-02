// Package parquet holds the rotating parquet sink and reader used by the admin
// to persist the three observability signals. It is instantiated only in the
// admin process; the gateway never imports it.
package parquet

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/parquet-go/parquet-go"
)

// Sink[Row] buffers rows in memory and flushes a NEW parquet file when the
// hour boundary crosses or len(buf) reaches maxRows. A closed parquet file is
// immutable, so we never append to one — every flush writes a brand-new file
// to <dir>/<signal>/<YYYYMMDDHH>-<seq>.parquet.tmp and atomically renames it.
type Sink[Row any] struct {
	dir     string
	signal  string
	maxRows int

	mu      sync.Mutex
	buf     []Row
	curHour int64
	seq     int64
}

func NewSink[Row any](dir, signal string, maxRows int) (*Sink[Row], error) {
	if maxRows <= 0 {
		maxRows = 50000
	}
	if err := os.MkdirAll(filepath.Join(dir, signal), 0o755); err != nil {
		return nil, err
	}
	return &Sink[Row]{dir: dir, signal: signal, maxRows: maxRows}, nil
}

func (s *Sink[Row]) Write(rows []Row) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range rows {
		s.buf = append(s.buf, r)
		if len(s.buf) >= s.maxRows {
			if err := s.flushLocked(time.Now()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Sink[Row]) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushLocked(time.Now())
}

func (s *Sink[Row]) Close() error {
	return s.Flush()
}

// flushLocked writes buf (if any) to a new file. Caller holds s.mu.
func (s *Sink[Row]) flushLocked(now time.Time) error {
	if len(s.buf) == 0 {
		return nil
	}
	hour := now.UTC().Truncate(time.Hour).UnixNano()
	if hour != s.curHour {
		s.seq = 0
		s.curHour = hour
	}
	s.seq++
	name := filepath.Join(s.dir, s.signal,
		fmt.Sprintf("%s-%04d.parquet", time.Unix(0, hour).UTC().Format("2006010215"), s.seq))
	tmp := name + ".tmp"

	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	pw := parquet.NewGenericWriter[Row](f)
	if _, err := pw.Write(s.buf); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := pw.Close(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, name); err != nil {
		return err
	}
	s.buf = s.buf[:0]
	return nil
}

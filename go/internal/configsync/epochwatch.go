package configsync

import (
	"context"
	"log"
	"strconv"
	"sync/atomic"
	"time"
)

// Notifier is implemented by ConfigServer. EpochWatcher calls Notify exactly
// once per epoch advance, regardless of whether the write happened on this
// replica or was observed by polling shared storage.
type Notifier interface {
	Notify()
}

// EpochStore reads the shared config_epoch setting. Satisfied by
// storage.SettingsStore.
type EpochStore interface {
	Get(key string) (string, error)
}

// EpochWatcher deduplicates config-change notifications across admin
// replicas sharing one database: a write on any replica bumps config_epoch
// in storage, and every replica converges on calling Notify() exactly once
// for that epoch — the writer via an immediate Observe call, every other
// replica via Run's poll loop. There is no leader; replicas are peers that
// each watch the same epoch value.
type EpochWatcher struct {
	store    EpochStore
	notifier Notifier

	lastEpoch atomic.Int64
}

// NewEpochWatcher builds a watcher over store, pushing through notifier.
func NewEpochWatcher(store EpochStore, notifier Notifier) *EpochWatcher {
	return &EpochWatcher{store: store, notifier: notifier}
}

// Seed reads the current config_epoch and initializes lastEpoch without
// notifying. Call once at startup, before Run: gateways already get the
// current snapshot on connect, so re-pushing on the first poll tick would
// just be a redundant no-op push.
func (w *EpochWatcher) Seed(ctx context.Context) error {
	epoch, err := w.readEpoch()
	if err != nil {
		return err
	}
	w.lastEpoch.Store(epoch)
	return nil
}

// Observe advances lastEpoch and calls Notify if epoch is newer than what
// this watcher has already pushed. Safe for concurrent callers: only the
// call that wins the compare-and-swap invokes Notify, so a given epoch is
// pushed at most once per watcher no matter how many callers observe it.
func (w *EpochWatcher) Observe(epoch int64) {
	for {
		last := w.lastEpoch.Load()
		if epoch <= last {
			return
		}
		if w.lastEpoch.CompareAndSwap(last, epoch) {
			w.notifier.Notify()
			return
		}
	}
}

// Run polls config_epoch every interval and calls Observe on each read,
// picking up changes committed by any replica sharing the same store.
// Blocks until ctx is cancelled. Call Seed before Run. interval <= 0
// disables polling and Run returns immediately — appropriate for
// single-instance/standalone deployments where no other replica can bump
// the epoch out from under this one.
func (w *EpochWatcher) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			epoch, err := w.readEpoch()
			if err != nil {
				log.Printf("configsync: epoch poll failed: %v", err)
				continue
			}
			w.Observe(epoch)
		}
	}
}

func (w *EpochWatcher) readEpoch() (int64, error) {
	v, err := w.store.Get("config_epoch")
	if err != nil {
		return 0, err
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n, nil
}

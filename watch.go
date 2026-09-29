package kernel

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// reloadDebounce coalesces bursts of change events (editors commonly write a
// file several times in quick succession).
const reloadDebounce = 100 * time.Millisecond

// reloadWatcher drives automatic reloads for one Reloadable config binding:
// file sources are watched with fsnotify, WatchSource implementations push
// change notifications. Changes trigger a debounced full rebuild.
type reloadWatcher struct {
	cb     *configBinding
	logger *slog.Logger

	mu      sync.Mutex
	stopped bool
	cancel  context.CancelFunc
	fw      *fsnotify.Watcher
	timer   *time.Timer
}

func newReloadWatcher(cb *configBinding, logger *slog.Logger) *reloadWatcher {
	return &reloadWatcher{cb: cb, logger: logger}
}

// start registers the watches synchronously (so changes right after Build are
// not missed by the async OS watch setup), launches the event pumps, and fires
// one initial reload to converge on anything that changed while starting.
func (w *reloadWatcher) start() {
	ctx, cancel := context.WithCancel(context.Background())
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		cancel()
		return
	}
	w.cancel = cancel
	w.mu.Unlock()

	paths := w.cb.filePaths

	if len(paths) > 0 {
		fw, err := fsnotify.NewWatcher()
		if err != nil {
			w.fail(err)
		} else {
			w.mu.Lock()
			w.fw = fw
			w.mu.Unlock()
			for _, p := range paths {
				// Watch the parent directory so atomic-rename saves (vim, sed -i)
				// are still noticed.
				if addErr := fw.Add(filepath.Dir(p)); addErr != nil {
					w.logger.Debug("kernel: cannot watch config directory", "dir", filepath.Dir(p), "error", addErr)
				}
			}
			go w.loopFS(ctx, fw, paths)
		}
	}

	for _, s := range w.cb.sources {
		ws, ok := s.(WatchSource)
		if !ok {
			continue
		}
		go func(ws WatchSource) {
			if err := ws.Watch(ctx, func(werr error) {
				if werr != nil {
					w.fail(werr)
					return
				}
				w.debounce()
			}); err != nil {
				w.fail(err)
			}
		}(ws)
	}

	go func() {
		<-ctx.Done()
		w.mu.Lock()
		if w.fw != nil {
			_ = w.fw.Close()
		}
		w.mu.Unlock()
	}()

	// Converge on anything that changed while the watches were being set up.
	w.debounce()
}

func (w *reloadWatcher) stop() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	w.stopped = true
	if w.cancel != nil {
		w.cancel()
	}
	if w.timer != nil {
		w.timer.Stop()
	}
	w.mu.Unlock()
}

func (w *reloadWatcher) loopFS(ctx context.Context, fw *fsnotify.Watcher, paths []string) {
	bases := make(map[string]bool, len(paths))
	for _, p := range paths {
		bases[filepath.Base(p)] = true
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-fw.Events:
			if !ok {
				return
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			if !bases[filepath.Base(ev.Name)] {
				continue
			}
			w.debounce()
		case err, ok := <-fw.Errors:
			if !ok {
				return
			}
			w.fail(err)
		}
	}
}

func (w *reloadWatcher) debounce() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(reloadDebounce, w.doReload)
	w.mu.Unlock()
}

func (w *reloadWatcher) doReload() {
	v, err := w.cb.buildValue()
	if err != nil {
		w.fail(err)
		return
	}
	w.cb.monSet(v)
	w.logger.Debug("kernel: config reloaded", "section", w.cb.section)
}

func (w *reloadWatcher) fail(err error) {
	if w.cb.onReloadError != nil {
		w.cb.onReloadError(err)
	}
	w.logger.Error("kernel: config reload failed", "section", w.cb.section, "error", err)
}

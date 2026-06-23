// Package watcher provides a recursive inotify-based file watcher with
// per-file event debouncing. On Linux, fsnotify uses inotify under the hood.
package watcher

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// EventKind classifies what happened to a file.
type EventKind int

const (
	EventWrite  EventKind = iota // file was created or modified
	EventDelete                  // file was removed or renamed away
)

// Event is emitted to the Handler for each file-level change.
type Event struct {
	Kind EventKind
	Path string
}

// Handler is called from a dedicated goroutine after the debounce delay.
type Handler func(Event)

// Watcher watches a directory tree recursively.
type Watcher struct {
	fsw      *fsnotify.Watcher
	handler  Handler
	debounce time.Duration

	mu     sync.Mutex
	timers map[string]*time.Timer

	log *slog.Logger
}

// New creates a Watcher.
// debounce is how long to wait after the last event before firing the handler.
// 2 seconds works well for most editors.
func New(handler Handler, debounce time.Duration, log *slog.Logger) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		fsw:      fsw,
		handler:  handler,
		debounce: debounce,
		timers:   make(map[string]*time.Timer),
		log:      log,
	}
	return w, nil
}

// Watch adds vaultPath and all its subdirectories to the watch list,
// then starts the event loop in a background goroutine.
// Returns once watching is established; the goroutine runs until Close().
func (w *Watcher) Watch(vaultPath string) error {
	// Register all existing directories (fsnotify watches don't recurse on their own)
	err := filepath.WalkDir(vaultPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if watchErr := w.fsw.Add(path); watchErr != nil {
			w.log.Warn("watch add failed", "path", path, "err", watchErr)
		}
		return nil
	})
	if err != nil {
		return err
	}

	go w.loop()
	w.log.Info("inotify watcher started", "path", vaultPath, "debounce", w.debounce)
	return nil
}

// Close shuts down the watcher and cancels all pending debounce timers.
func (w *Watcher) Close() error {
	w.mu.Lock()
	for _, t := range w.timers {
		t.Stop()
	}
	w.mu.Unlock()
	return w.fsw.Close()
}

// ── event loop ────────────────────────────────────────────────────────────────

func (w *Watcher) loop() {
	for {
		select {
		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.dispatch(event)

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			w.log.Error("fsnotify error", "err", err)
		}
	}
}

func (w *Watcher) dispatch(ev fsnotify.Event) {
	path := ev.Name

	switch {
	case ev.Has(fsnotify.Create):
		if isDir(path) {
			// New directory: add it to the watch list immediately so future
			// files created inside are picked up.
			if err := w.fsw.Add(path); err != nil {
				w.log.Warn("watching new directory", "path", path, "err", err)
			}
			// Walk sub-tree in case it was moved in wholesale (e.g. cp -r)
			_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
				if err != nil || !d.IsDir() || strings.HasPrefix(d.Name(), ".") {
					return nil
				}
				_ = w.fsw.Add(p)
				return nil
			})
			return
		}
		w.scheduleEvent(path, EventWrite)

	case ev.Has(fsnotify.Write):
		if !isDir(path) {
			w.scheduleEvent(path, EventWrite)
		}

	case ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename):
		// On rename, the old path is reported as RENAME; the new path appears
		// as a separate CREATE event, so we only need to handle the deletion here.
		w.scheduleEvent(path, EventDelete)
	}
}

// scheduleEvent arms (or resets) a debounce timer for the given path.
// Only the last event kind within the debounce window is forwarded.
func (w *Watcher) scheduleEvent(path string, kind EventKind) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if t, ok := w.timers[path]; ok {
		t.Stop()
	}

	w.timers[path] = time.AfterFunc(w.debounce, func() {
		w.mu.Lock()
		delete(w.timers, path)
		w.mu.Unlock()

		w.log.Debug("file event fired", "path", path, "kind", kind)
		w.handler(Event{Kind: kind, Path: path})
	})
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

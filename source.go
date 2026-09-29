package kernel

import "context"

// Source is a pull-based configuration source. Load returns the source's
// settings as a nested map; the framework flattens and merges it with the
// other sources of a Config binding.
type Source interface {
	Name() string
	Load() (map[string]any, error)
}

// WatchSource is a Source that can also push change notifications, used by
// remote sources such as ETCD. Under the WithReloadable option, WatchSource
// implementations are watched via Watch; plain Sources (files) are watched
// with fsnotify instead.
type WatchSource interface {
	Source
	// Watch starts watching the source until ctx is done. onChange is invoked
	// with a non-nil error when the watch fails irrecoverably, and with nil
	// whenever the underlying data changed.
	Watch(ctx context.Context, onChange func(error)) error
}

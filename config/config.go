// Package config provides the built-in configuration options for
// AppBuilder.Config[T] and the standalone kernel.LoadConfig function.
//
//	import (
//	    "github.com/gocrud/kernel"
//	    config "github.com/gocrud/kernel/config"
//	)
//
//	b.Config[AppConfig]("app",
//	    config.WithDefaults(func(c *AppConfig) { c.Port = 8080 }),
//	    config.WithFile("./config/app.yaml"),
//	    config.WithEnv("APP_"),
//	    config.WithReloadable,
//	)
package config

import (
	"flag"

	"github.com/gocrud/kernel"
)

// optionFunc adapts a closure to the kernel.ConfigOption interface.
type optionFunc func(spec *kernel.BindSpec)

func (f optionFunc) Apply(spec *kernel.BindSpec) { f(spec) }

// WithFile adds a configuration file, decoded by extension (.json/.yaml/.yml/.toml).
// Under WithReloadable, the file is watched with fsnotify and reloaded on change.
func WithFile(path string) kernel.ConfigOption {
	return optionFunc(func(spec *kernel.BindSpec) {
		spec.Sources = append(spec.Sources, &fileSource{path: path})
		spec.FilePaths = append(spec.FilePaths, path)
	})
}

// WithMap adds an in-memory map source.
func WithMap(m map[string]any) kernel.ConfigOption {
	return optionFunc(func(spec *kernel.BindSpec) {
		spec.Sources = append(spec.Sources, &mapSource{m: m, name: "map"})
	})
}

// WithEnv adds environment variables whose names start with prefix; APP_PORT maps
// to app.port. The prefix only filters which variables are included.
func WithEnv(prefix string) kernel.ConfigOption {
	return optionFunc(func(spec *kernel.BindSpec) {
		spec.Sources = append(spec.Sources, &envSource{prefix: prefix})
	})
}

// WithFlag adds the currently-set flags of fs, mapped by flag name.
func WithFlag(fs *flag.FlagSet) kernel.ConfigOption {
	return optionFunc(func(spec *kernel.BindSpec) {
		spec.Sources = append(spec.Sources, &flagSource{fs: fs})
	})
}

// WithSource adds any custom kernel.Source.
func WithSource(s kernel.Source) kernel.ConfigOption {
	return optionFunc(func(spec *kernel.BindSpec) {
		spec.Sources = append(spec.Sources, s)
	})
}

// WithReloadable opts the binding into dynamic updates: *kernel.ConfigMonitor[T]
// is registered in addition to *kernel.Config[T], and file sources (fsnotify)
// plus WatchSource implementations (push) trigger automatic reloads. Disabled
// by default. Reload failures keep the previous value and are reported to the
// logger and the optional WithOnReloadError callback.
var WithReloadable kernel.ConfigOption = optionFunc(func(spec *kernel.BindSpec) { spec.Reloadable = true })

// WithOnReloadError registers a callback for reload failures (parse errors, I/O
// errors, binding errors). The previous value is kept on failure.
func WithOnReloadError(fn func(error)) kernel.ConfigOption {
	return optionFunc(func(spec *kernel.BindSpec) { spec.OnReloadError = fn })
}

// defaultsOption carries typed default callbacks. It implements
// kernel.DefaultsApplier so the callbacks are applied directly to the binding
// target without leaking a boxed func(any) into the public kernel.BindSpec.
type defaultsOption[T any] struct {
	fns []func(*T)
}

func (o *defaultsOption[T]) Apply(spec *kernel.BindSpec) {
	// Default callbacks travel via ApplyDefaults, keeping BindSpec free of type-erased
	// callbacks; Apply is a no-op on the spec.
}

func (o *defaultsOption[T]) ApplyDefaults(target any) {
	t := target.(*T)
	for _, fn := range o.fns {
		fn(t)
	}
}

// WithDefaults applies typed default values before any source is merged: keys not
// provided by any source keep their default value.
func WithDefaults[T any](fns ...func(*T)) kernel.ConfigOption {
	return &defaultsOption[T]{fns: fns}
}

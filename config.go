package kernel

import (
	"log/slog"
	"reflect"
)

// BindSpec is the mutable specification that ConfigOption.Apply customizes.
// Any package can implement options (the built-ins live in kernel/config).
type BindSpec struct {
	// Sources are loaded and merged in order; later sources win per key.
	Sources []Source
	// FilePaths lists the configuration file paths for fsnotify watching,
	// filled by the config.WithFile option.
	FilePaths []string
	// Reloadable opts the binding into dynamic updates.
	Reloadable bool
	// OnReloadError receives reload failures (the previous value is kept).
	OnReloadError func(error)
}

// ConfigOption customizes an AppBuilder.Config[T] binding. Any package can implement it;
// kernel/config provides the built-in options.
type ConfigOption interface {
	// Apply mutates spec to customize the binding.
	Apply(spec *BindSpec)
}

// DefaultsApplier is an optional interface implemented by options that
// contribute typed default callbacks, such as config.WithDefaults. ApplyDefaults
// receives *T, the address of the binding's zero-valued target, and is invoked
// before any source is merged.
type DefaultsApplier interface {
	ApplyDefaults(target any) // target is *T of the binding's T
}

// configBinding is the frozen description of one AppBuilder.Config call.
type configBinding struct {
	section  string
	t        reflect.Type
	optsType reflect.Type // *Config[T]
	monType  reflect.Type // *ConfigMonitor[T]

	defaults      []func(any)
	sources       []Source
	filePaths     []string
	reloadable    bool
	onReloadError func(error)
	logger        *slog.Logger

	// The typed closures below are created in the generic Config/LoadConfig context.
	buildOpts    func() (any, error) // returns *Config[T]
	buildMonitor func() (any, error) // returns *ConfigMonitor[T]
	buildValue   func() (any, error) // returns the bound T (used for reloads)
	mon          any                 // the *ConfigMonitor[T] registered in the app
	monSet       func(v any)         // mon.(*ConfigMonitor[T]).Set(v.(T))
}

func newConfigBinding[T any](b *AppBuilder, section string, opts []ConfigOption) *configBinding {
	t := reflect.TypeFor[T]()
	spec := &BindSpec{}
	var appliers []DefaultsApplier
	for _, o := range opts {
		o.Apply(spec)
		if d, ok := o.(DefaultsApplier); ok {
			appliers = append(appliers, d)
		}
	}

	logger := slog.Default()
	if b != nil {
		logger = b.loggerOrDefault()
	}
	cb := &configBinding{
		section:       section,
		t:             t,
		optsType:      reflect.TypeFor[*Config[T]](),
		monType:       reflect.TypeFor[*ConfigMonitor[T]](),
		defaults:      adaptDefaultAppliers(appliers),
		sources:       spec.Sources,
		filePaths:     spec.FilePaths,
		reloadable:    spec.Reloadable,
		onReloadError: spec.OnReloadError,
		logger:        logger,
	}
	cb.buildValue = func() (any, error) {
		v, err := buildBoundValue[T](cb)
		if err != nil {
			return nil, err
		}
		return v, nil
	}
	cb.buildOpts = func() (any, error) {
		v, err := cb.buildValue()
		if err != nil {
			return nil, err
		}
		return &Config[T]{Value: v.(T)}, nil
	}
	cb.buildMonitor = func() (any, error) {
		v, err := cb.buildValue()
		if err != nil {
			return nil, err
		}
		return newConfigMonitor(v.(T)), nil
	}
	cb.monSet = func(v any) {
		if cb.mon == nil {
			return
		}
		m := cb.mon.(*ConfigMonitor[T])
		if reflect.DeepEqual(m.CurrentValue(), v.(T)) {
			return // no-op reloads do not notify listeners
		}
		m.Set(v.(T))
	}
	return cb
}

// adaptDefaultAppliers binds each DefaultsApplier's ApplyDefaults method as a
// func(any) for the binding's internal use. The boxing stays private.
func adaptDefaultAppliers(appliers []DefaultsApplier) []func(any) {
	if len(appliers) == 0 {
		return nil
	}
	fns := make([]func(any), len(appliers))
	for i, d := range appliers {
		fns[i] = d.ApplyDefaults
	}
	return fns
}

// Config registers configuration for T in one step: it merges the given
// sources in registration order (later sources override earlier ones for the
// same key), binds the section to T, and registers the result as a Singleton
// *Config[T]. With the WithReloadable option (see kernel/config) it additionally
// registers a Singleton *ConfigMonitor[T] that automatically receives updates.
func (b *AppBuilder) Config[T any](section string, opts ...ConfigOption) *AppBuilder {
	b.configBindings = append(b.configBindings, newConfigBinding[T](b, section, opts))
	return b
}

// LoadConfig loads and binds T once, outside of DI; it is the standalone
// counterpart of AppBuilder.Config. WithReloadable is ignored.
func LoadConfig[T any](section string, opts ...ConfigOption) (*T, error) {
	cb := newConfigBinding[T](nil, section, opts)
	v, err := buildBoundValue[T](cb)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// buildBoundValue computes the bound T: defaults are applied to a zero value,
// then every source is merged in order and the section is bound onto the
// defaulted value (keys absent from all sources keep their defaults).
func buildBoundValue[T any](cb *configBinding) (T, error) {
	var zero T
	v := reflect.New(cb.t).Elem()
	for _, fn := range cb.defaults {
		fn(v.Addr().Interface())
	}

	merged := make(map[string]any)
	for _, s := range cb.sources {
		m, err := s.Load()
		if err != nil {
			return zero, &ConfigError{Source: s.Name(), Err: err}
		}
		mergeInto(merged, flatten(m))
	}

	raw, ok := sectionValue(merged, cb.section)
	if !ok {
		return v.Interface().(T), nil // no values for this section: defaults only
	}
	if m, isMap := raw.(map[string]any); isMap {
		raw = nest(m)
	}
	if err := fillValue(v, raw, cb.section); err != nil {
		return zero, &ConfigError{Source: "section " + cb.section, Err: err}
	}
	return v.Interface().(T), nil
}

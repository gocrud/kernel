package kernel

import (
	"log/slog"
	"reflect"
)

// Extension is the convention for reusable, chainable registration groups.
// Third-party packages implement plain functions of this shape using only
// AppBuilder's public methods, e.g. AddXxx(b *AppBuilder). An Extension only
// performs registrations; it needs no return value.
type Extension func(*AppBuilder)

// AppBuilder collects service registrations and configuration bindings
// before they are built into an App.
type AppBuilder struct {
	descriptors      map[reflect.Type][]*descriptor
	keyedDescriptors map[reflect.Type]map[any][]*descriptor
	optionConfigs    map[reflect.Type][]func(any)
	monitorConfigs   map[reflect.Type][]func(any)
	configBindings   []*configBinding
	logger           *slog.Logger
}

// New creates an empty AppBuilder.
func New() *AppBuilder {
	return &AppBuilder{
		descriptors:      make(map[reflect.Type][]*descriptor),
		keyedDescriptors: make(map[reflect.Type]map[any][]*descriptor),
		optionConfigs:    make(map[reflect.Type][]func(any)),
		monitorConfigs:   make(map[reflect.Type][]func(any)),
	}
}

func (b *AppBuilder) isRegistered(t reflect.Type) bool {
	return len(b.descriptors[t]) > 0
}

func (b *AppBuilder) isKeyedRegistered(t reflect.Type, key any) bool {
	return len(b.keyedDescriptors[t][key]) > 0
}

// Provide registers TService. factory is either a pre-built instance or an
// auto-wired constructor func(deps...) T / func(deps...) (T, error); every
// parameter type must itself be registered. Everything is constructed eagerly
// by Build. Provider[T] is auto-registered alongside T.
func (b *AppBuilder) Provide[T any](factory any, opts ...ProvideOption) *AppBuilder {
	t := reflect.TypeFor[T]()
	d := buildDescriptor(t, factory)
	applyProvideOptions(d, opts)
	b.descriptors[t] = append(b.descriptors[t], d)
	b.ensureProvider[T]()
	return b
}

func applyProvideOptions(d *descriptor, opts []ProvideOption) {
	if len(opts) == 0 {
		return
	}
	spec := &ProvideSpec{}
	for _, o := range opts {
		o.Apply(spec)
	}
	d.closeOnStop = spec.CloseOnStop
}

// TryProvide registers TService only if it has no existing registration.
func (b *AppBuilder) TryProvide[T any](factory any, opts ...ProvideOption) *AppBuilder {
	t := reflect.TypeFor[T]()
	if b.isRegistered(t) {
		return b
	}
	return b.Provide[T](factory, opts...)
}

// ProvideKeyed registers TService under key. Keyed registrations are only
// resolvable via GetKeyed/MustGetKeyed/GetAllKeyed (and the
// injectable Keyed[T] accessor), never via unkeyed resolution. key must be a
// comparable value. Keyed[T] is auto-registered alongside the first keyed
// registration of T.
func (b *AppBuilder) ProvideKeyed[T any](key any, factory any, opts ...ProvideOption) *AppBuilder {
	t := reflect.TypeFor[T]()
	if b.keyedDescriptors[t] == nil {
		b.keyedDescriptors[t] = make(map[any][]*descriptor)
	}
	d := buildDescriptor(t, factory)
	d.key = key
	applyProvideOptions(d, opts)
	b.keyedDescriptors[t][key] = append(b.keyedDescriptors[t][key], d)
	b.ensureKeyed[T]()
	return b
}

// TryProvideKeyed registers TService/key only if that exact pair has no
// existing registration.
func (b *AppBuilder) TryProvideKeyed[T any](key any, factory any, opts ...ProvideOption) *AppBuilder {
	t := reflect.TypeFor[T]()
	if b.isKeyedRegistered(t, key) {
		return b
	}
	return b.ProvideKeyed[T](key, factory, opts...)
}

// Decorate wraps the last registration of TService. decoratorFn's first
// parameter receives the originally constructed instance; additional
// parameters are auto-wired dependencies. Multiple Decorate calls stack in
// registration order (later calls wrap further out).
func (b *AppBuilder) Decorate[T any](decoratorFn any) *AppBuilder {
	t := reflect.TypeFor[T]()
	descs := b.descriptors[t]
	if len(descs) == 0 {
		panic("kernel: cannot decorate unregistered service " + t.String())
	}
	d := descs[len(descs)-1]
	d.decorators = append(d.decorators, buildDecorator(t, decoratorFn))
	return b
}

// Extend applies ext to b and returns b, enabling
// b.Provide[...](...).Extend(mypkg.AddXxx).Provide[...](...) chaining.
func (b *AppBuilder) Extend(ext Extension) *AppBuilder {
	ext(b)
	return b
}

// WithLogger sets the *slog.Logger used for framework diagnostics. It is also
// auto-registered for injection unless *slog.Logger was explicitly provided.
func (b *AppBuilder) WithLogger(l *slog.Logger) *AppBuilder {
	b.logger = l
	return b
}

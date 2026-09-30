package kernel

import (
	"fmt"
	"reflect"
)

// Provider is an injectable factory delegate for the unkeyed singleton of T.
// Calling Get is an O(1) lookup with no reflection, locking or construction;
// the framework auto-registers Provider[T] whenever T is registered.
type Provider[T any] struct {
	fn func() (T, error)
}

// Get returns the singleton registered for T.
func (p Provider[T]) Get() (T, error) {
	var zero T
	if p.fn == nil {
		return zero, fmt.Errorf("%w: %s", ErrServiceNotRegistered, reflect.TypeFor[T]())
	}
	return p.fn()
}

// MustGet is Get, but panics on error.
func (p Provider[T]) MustGet() T {
	v, err := p.Get()
	if err != nil {
		panic(err)
	}
	return v
}

// Keyed is an injectable accessor over the keyed registrations of T. It lets a
// constructor resolve keyed singletons inside its body (keys may even be
// config-driven), while the dependency graph still knows that every keyed
// registration of T is constructed first. Auto-registered by ProvideKeyed[T].
type Keyed[T any] struct {
	get func(key any) (T, error)
	all func(key any) []T
}

// Get resolves the last registration of T made under key.
func (k Keyed[T]) Get(key any) (T, error) {
	return k.get(key)
}

// MustGet is Get, but panics on error.
func (k Keyed[T]) MustGet(key any) T {
	v, err := k.Get(key)
	if err != nil {
		panic(err)
	}
	return v
}

// All returns every registration of T made under key, in registration order.
func (k Keyed[T]) All(key any) []T {
	return k.all(key)
}

// ensureProvider auto-registers Provider[T] with TryAdd semantics. It must be
// called from a generic context so the Provider[T] type and its factory closure
// can be built statically.
func (b *AppBuilder) ensureProvider[T any]() {
	pt := reflect.TypeFor[Provider[T]]()
	if b.isRegistered(pt) {
		return
	}
	d := &descriptor{
		serviceType: pt,
		isAccessor:  true,
		paramTypes:  []reflect.Type{reflect.TypeFor[T]()},
		accessorFactory: func(app *App) any {
			return Provider[T]{fn: func() (T, error) { return app.Get[T]() }}
		},
	}
	b.descriptors[pt] = append(b.descriptors[pt], d)
}

// ensureKeyed auto-registers Keyed[T] with TryAdd semantics.
func (b *AppBuilder) ensureKeyed[T any]() {
	kt := reflect.TypeFor[Keyed[T]]()
	if b.isRegistered(kt) {
		return
	}
	d := &descriptor{
		serviceType: kt,
		isAccessor:  true,
		keyedOf:     reflect.TypeFor[T](),
		accessorFactory: func(app *App) any {
			return Keyed[T]{
				get: func(key any) (T, error) { return app.GetKeyed[T](key) },
				all: func(key any) []T { return app.GetAllKeyed[T](key) },
			}
		},
	}
	b.descriptors[kt] = append(b.descriptors[kt], d)
}

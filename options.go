package kernel

import (
	"reflect"
	"sync"
	"sync/atomic"
)

// Options wraps a configured value of type T, the static, read-only shape of
// configuration. Analogous to .NET's IOptions[T].
type Options[T any] struct {
	Value T
}

// Configure registers a configuration callback for T. Multiple Configure calls
// for the same T apply in registration order against a zero-value T, then the
// result is exposed as a Singleton *Options[T]. Must be called before Build.
func (b *ContainerBuilder) Configure[T any](configure func(*T)) *ContainerBuilder {
	t := reflect.TypeFor[T]()
	b.optionConfigs[t] = append(b.optionConfigs[t], func(v any) { configure(v.(*T)) })

	b.TryProvide[*Options[T]](func() (*Options[T], error) {
		var value T
		for _, fn := range b.optionConfigs[t] {
			fn(&value)
		}
		return &Options[T]{Value: value}, nil
	})
	return b
}

// OptionsMonitor holds a live value of T that can be updated at runtime via
// Set, analogous to .NET's IOptionsMonitor[T]. The framework does not know how
// or when the value should change: either call Set manually, or enable the
// WithReloadable Config option to have file / ETCD sources push changes
// automatically.
type OptionsMonitor[T any] struct {
	value     atomic.Pointer[T]
	mu        sync.Mutex // guards listeners only, never held while invoking them
	listeners map[int]func(T)
	nextID    int
}

// CurrentValue returns the most recently set value. Lock-free read.
func (m *OptionsMonitor[T]) CurrentValue() T {
	return *m.value.Load()
}

// OnChange registers a listener invoked after every Set call. The returned
// func unsubscribes it.
func (m *OptionsMonitor[T]) OnChange(listener func(T)) (unsubscribe func()) {
	m.mu.Lock()
	id := m.nextID
	m.nextID++
	m.listeners[id] = listener
	m.mu.Unlock()

	return func() {
		m.mu.Lock()
		delete(m.listeners, id)
		m.mu.Unlock()
	}
}

// Set stores a new value and notifies all current listeners with it. The
// listeners are invoked without holding the internal lock, so they may safely
// call OnChange/Set again.
func (m *OptionsMonitor[T]) Set(newValue T) {
	m.value.Store(&newValue)

	m.mu.Lock()
	snapshot := make([]func(T), 0, len(m.listeners))
	for _, l := range m.listeners {
		snapshot = append(snapshot, l)
	}
	m.mu.Unlock()

	for _, l := range snapshot {
		l(newValue)
	}
}

func newOptionsMonitor[T any](initial T) *OptionsMonitor[T] {
	m := &OptionsMonitor[T]{listeners: make(map[int]func(T))}
	m.value.Store(&initial)
	return m
}

// ConfigureMonitor registers a configuration callback for T, exposed as a
// Singleton *OptionsMonitor[T]. Multiple calls apply in registration order to
// compute the initial value. Runtime updates are pushed via Set.
func (b *ContainerBuilder) ConfigureMonitor[T any](configure func(*T)) *ContainerBuilder {
	t := reflect.TypeFor[T]()
	b.monitorConfigs[t] = append(b.monitorConfigs[t], func(v any) { configure(v.(*T)) })

	b.TryProvide[*OptionsMonitor[T]](func() (*OptionsMonitor[T], error) {
		var value T
		for _, fn := range b.monitorConfigs[t] {
			fn(&value)
		}
		return newOptionsMonitor(value), nil
	})
	return b
}

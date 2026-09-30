package kernel

import (
	"reflect"
	"sync"
	"sync/atomic"
)

// Config wraps a configured value of type T, the static, read-only shape of
// configuration.
type Config[T any] struct {
	Value T
}

// Configure registers a configuration callback for T. Multiple Configure calls
// for the same T apply in registration order against a zero-value T, then the
// result is exposed as a Singleton *Config[T]. Must be called before Build.
func (b *AppBuilder) Configure[T any](configure func(*T)) *AppBuilder {
	t := reflect.TypeFor[T]()
	b.optionConfigs[t] = append(b.optionConfigs[t], func(v any) { configure(v.(*T)) })

	b.TryProvide[*Config[T]](func() (*Config[T], error) {
		var value T
		for _, fn := range b.optionConfigs[t] {
			fn(&value)
		}
		return &Config[T]{Value: value}, nil
	})
	return b
}

// ConfigMonitor holds a live value of T that can be updated at runtime via
// Set. The framework does not know how or when the value should change:
// either call Set manually, or enable the WithReloadable Load option to have
// file sources (or a custom WatchSource) push changes automatically.
type ConfigMonitor[T any] struct {
	value     atomic.Pointer[T]
	mu        sync.Mutex // guards listeners only, never held while invoking them
	listeners map[int]func(T)
	nextID    int
}

// CurrentValue returns the most recently set value. Lock-free read.
func (m *ConfigMonitor[T]) CurrentValue() T {
	return *m.value.Load()
}

// OnChange registers a listener invoked after every Set call. The returned
// func unsubscribes it.
func (m *ConfigMonitor[T]) OnChange(listener func(T)) (unsubscribe func()) {
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
func (m *ConfigMonitor[T]) Set(newValue T) {
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

func newConfigMonitor[T any](initial T) *ConfigMonitor[T] {
	m := &ConfigMonitor[T]{listeners: make(map[int]func(T))}
	m.value.Store(&initial)
	return m
}

// ConfigureMonitor registers a configuration callback for T, exposed as a
// Singleton *ConfigMonitor[T]. Multiple calls apply in registration order to
// compute the initial value. Runtime updates are pushed via Set.
func (b *AppBuilder) ConfigureMonitor[T any](configure func(*T)) *AppBuilder {
	t := reflect.TypeFor[T]()
	b.monitorConfigs[t] = append(b.monitorConfigs[t], func(v any) { configure(v.(*T)) })

	b.TryProvide[*ConfigMonitor[T]](func() (*ConfigMonitor[T], error) {
		var value T
		for _, fn := range b.monitorConfigs[t] {
			fn(&value)
		}
		return newConfigMonitor(value), nil
	})
	return b
}

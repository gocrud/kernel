package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"sync"
)

// Container resolves services registered in a ContainerBuilder and eagerly
// built by Build. Every service is a Singleton constructed once, in dependency
// order, during Build, so resolution methods never construct anything: they
// only read values Build already produced.
type Container struct {
	entries []*descriptor

	typeIndex   map[reflect.Type]int
	typeIndices map[reflect.Type][]int

	keyedIndex   map[reflect.Type]map[any]int
	keyedIndices map[reflect.Type]map[any][]int

	stoppables []stoppable // construction order; Stop disposes in reverse
	startables []Startable // construction order; Start invokes in order

	stopOnce  sync.Once
	stopErr   error
	startOnce sync.Once
	startErr  error

	watchers []*reloadWatcher // Reloadable config watchers, stopped by Stop/Close

	logger *slog.Logger
}

// Logger returns the *slog.Logger used by the framework (and injectable as a
// singleton).
func (c *Container) Logger() *slog.Logger {
	if c.logger == nil {
		return slog.Default()
	}
	return c.logger
}

// Start invokes Start on every Startable singleton in construction order.
// Idempotent: only the first call actually starts anything.
func (c *Container) Start(ctx context.Context) error {
	c.startOnce.Do(func() {
		for _, s := range c.startables {
			if err := s.Start(ctx); err != nil {
				c.startErr = fmt.Errorf("kernel: starting %T: %w", s, err)
				return
			}
		}
	})
	return c.startErr
}

// Stop stops every singleton that implements Stoppable, plus every WithClose-marked
// io.Closer singleton, in the reverse of construction order, after stopping all
// Reloadable config watchers. Idempotent: only the first call actually stops anything.
func (c *Container) Stop(ctx context.Context) error {
	c.stopOnce.Do(func() {
		for _, w := range c.watchers {
			w.stop()
		}
		var errs []error
		for _, v := range slices.Backward(c.stoppables) {
			if err := v.stop(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		c.stopErr = errors.Join(errs...)
	})
	return c.stopErr
}

// Close is Stop with a background context; it satisfies io.Closer.
func (c *Container) Close() error {
	return c.Stop(context.Background())
}

// Resolve resolves the last registration of T. Returns ErrServiceNotRegistered
// if T was never registered.
func (c *Container) Resolve[T any]() (T, error) {
	var zero T
	t := reflect.TypeFor[T]()
	idx, ok := c.typeIndex[t]
	if !ok {
		return zero, fmt.Errorf("%w: %s", ErrServiceNotRegistered, t)
	}
	return c.entries[idx].singletonVal.(T), nil
}

// MustResolve resolves T like Resolve, but panics instead of returning an error.
func (c *Container) MustResolve[T any]() T {
	v, err := c.Resolve[T]()
	if err != nil {
		panic(err)
	}
	return v
}

// ResolveAll resolves every registration of T, in registration order.
func (c *Container) ResolveAll[T any]() ([]T, error) {
	t := reflect.TypeFor[T]()
	idxs := c.typeIndices[t]
	result := make([]T, len(idxs))
	for i, idx := range idxs {
		result[i] = c.entries[idx].singletonVal.(T)
	}
	return result, nil
}

// ResolveKeyed resolves the last registration of T made under key.
func (c *Container) ResolveKeyed[T any](key any) (T, error) {
	var zero T
	t := reflect.TypeFor[T]()
	idx, ok := c.keyedIndex[t][key]
	if !ok {
		return zero, fmt.Errorf("%w: %s (key=%v)", ErrServiceNotRegistered, t, key)
	}
	return c.entries[idx].singletonVal.(T), nil
}

// MustResolveKeyed resolves T under key like ResolveKeyed, but panics on error.
func (c *Container) MustResolveKeyed[T any](key any) T {
	v, err := c.ResolveKeyed[T](key)
	if err != nil {
		panic(err)
	}
	return v
}

// ResolveAllKeyed resolves every registration of T made under key, in
// registration order. Never returns an error: an unknown (T, key) pair yields
// an empty slice.
func (c *Container) ResolveAllKeyed[T any](key any) []T {
	t := reflect.TypeFor[T]()
	idxs := c.keyedIndices[t][key]
	result := make([]T, len(idxs))
	for i, idx := range idxs {
		result[i] = c.entries[idx].singletonVal.(T)
	}
	return result
}

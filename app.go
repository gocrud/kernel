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

// App hands out services registered in an AppBuilder and eagerly built by
// Build. Every service is a Singleton constructed once, in dependency order,
// during Build, so the Get methods never construct anything: they only read
// values Build already produced.
type App struct {
	entries []*descriptor

	typeIndex   map[reflect.Type]int
	typeIndices map[reflect.Type][]int

	keyedIndex   map[reflect.Type]map[any]int
	keyedIndices map[reflect.Type]map[any][]int

	stoppables []stopper // construction order; Stop disposes in reverse
	startables []Starter // construction order; Start invokes in order

	stopOnce  sync.Once
	stopErr   error
	startOnce sync.Once
	startErr  error

	watchers []*reloadWatcher // Reloadable config watchers, stopped by Stop/Close

	logger *slog.Logger
}

// Logger returns the *slog.Logger used by the framework (and injectable as a
// singleton).
func (app *App) Logger() *slog.Logger {
	if app.logger == nil {
		return slog.Default()
	}
	return app.logger
}

// Start invokes Start on every Starter singleton in construction order.
// Idempotent: only the first call actually starts anything.
func (app *App) Start(ctx context.Context) error {
	app.startOnce.Do(func() {
		for _, s := range app.startables {
			if err := s.Start(ctx); err != nil {
				app.startErr = fmt.Errorf("kernel: starting %T: %w", s, err)
				return
			}
		}
	})
	return app.startErr
}

// Stop stops every singleton that implements Stopper, plus every WithClose-marked
// io.Closer singleton, in the reverse of construction order, after stopping all
// Reloadable config watchers. Idempotent: only the first call actually stops anything.
func (app *App) Stop(ctx context.Context) error {
	app.stopOnce.Do(func() {
		for _, w := range app.watchers {
			w.stop()
		}
		var errs []error
		for _, v := range slices.Backward(app.stoppables) {
			if err := v.stop(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		app.stopErr = errors.Join(errs...)
	})
	return app.stopErr
}

// Close is Stop with a background context; it satisfies io.Closer.
func (app *App) Close() error {
	return app.Stop(context.Background())
}

// Get resolves the last registration of T. Returns ErrServiceNotRegistered
// if T was never registered.
func (app *App) Get[T any]() (T, error) {
	var zero T
	t := reflect.TypeFor[T]()
	idx, ok := app.typeIndex[t]
	if !ok {
		return zero, fmt.Errorf("%w: %s", ErrServiceNotRegistered, t)
	}
	return app.entries[idx].singletonVal.(T), nil
}

// MustGet is Get, but panics instead of returning an error.
func (app *App) MustGet[T any]() T {
	v, err := app.Get[T]()
	if err != nil {
		panic(err)
	}
	return v
}

// GetAll resolves every registration of T, in registration order.
func (app *App) GetAll[T any]() ([]T, error) {
	t := reflect.TypeFor[T]()
	idxs := app.typeIndices[t]
	result := make([]T, len(idxs))
	for i, idx := range idxs {
		result[i] = app.entries[idx].singletonVal.(T)
	}
	return result, nil
}

// GetKeyed resolves the last registration of T made under key.
func (app *App) GetKeyed[T any](key any) (T, error) {
	var zero T
	t := reflect.TypeFor[T]()
	idx, ok := app.keyedIndex[t][key]
	if !ok {
		return zero, fmt.Errorf("%w: %s (key=%v)", ErrServiceNotRegistered, t, key)
	}
	return app.entries[idx].singletonVal.(T), nil
}

// MustGetKeyed is GetKeyed, but panics on error.
func (app *App) MustGetKeyed[T any](key any) T {
	v, err := app.GetKeyed[T](key)
	if err != nil {
		panic(err)
	}
	return v
}

// GetAllKeyed resolves every registration of T made under key, in
// registration order. Never returns an error: an unknown (T, key) pair yields
// an empty slice.
func (app *App) GetAllKeyed[T any](key any) []T {
	t := reflect.TypeFor[T]()
	idxs := app.keyedIndices[t][key]
	result := make([]T, len(idxs))
	for i, idx := range idxs {
		result[i] = app.entries[idx].singletonVal.(T)
	}
	return result
}

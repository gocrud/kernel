package kernel

import (
	"context"
	"io"
)

// Startable is implemented by singletons that need an explicit startup phase.
// Container.Start calls Start on every Startable singleton in construction order.
type Startable interface {
	Start(ctx context.Context) error
}

// Stoppable is implemented by singletons that need context-aware shutdown.
// Container.Stop calls Stop in reverse construction order. Stoppable is
// recognized automatically; io.Closer is only honored when the registration
// carries the WithClose option. If a singleton implements both, only Stop
// is called (Close is ignored).
type Stoppable interface {
	Stop(ctx context.Context) error
}

// stoppable unifies Stoppable and WithClose-marked io.Closer singletons so
// both kinds are stopped in a single reverse-ordered list.
type stoppable struct {
	val any
}

func (s stoppable) stop(ctx context.Context) error {
	if st, ok := s.val.(Stoppable); ok {
		return st.Stop(ctx)
	}
	return s.val.(io.Closer).Close()
}

// ProvideSpec is the mutable specification that ProvideOption.Apply customizes.
// It is the open extension point for per-registration options on
// Provide/ProvideKeyed/TryProvide/TryProvideKeyed.
type ProvideSpec struct {
	// CloseOnStop registers the singleton for shutdown via io.Closer: during
	// Container.Stop its Close is called in reverse construction order.
	// Without this option io.Closer singletons are never touched by the
	// container. Stoppable always takes precedence over io.Closer.
	CloseOnStop bool
}

// ProvideOption customizes a single registration made through Provide,
// ProvideKeyed, TryProvide or TryProvideKeyed. kernel provides the built-in
// WithClose; any package can implement this interface.
type ProvideOption interface {
	Apply(spec *ProvideSpec)
}

type provideOptionFunc func(*ProvideSpec)

func (f provideOptionFunc) Apply(spec *ProvideSpec) { f(spec) }

// WithClose opts a registration into shutdown via io.Closer: Container.Stop
// calls its Close in reverse construction order. Without WithClose, a
// singleton that merely implements io.Closer is ignored by the container;
// Stoppable implementations are always stopped regardless.
func WithClose() ProvideOption {
	return provideOptionFunc(func(spec *ProvideSpec) { spec.CloseOnStop = true })
}

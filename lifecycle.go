package kernel

import (
	"context"
	"io"
)

// Starter is implemented by singletons that need an explicit startup phase.
// App.Start calls Start on every Starter singleton in construction order.
type Starter interface {
	Start(ctx context.Context) error
}

// Stopper is implemented by singletons that need context-aware shutdown.
// App.Stop calls Stop in reverse construction order. Stopper is
// recognized automatically; io.Closer is only honored when the registration
// carries the WithClose option. If a singleton implements both, only Stop
// is called (Close is ignored).
type Stopper interface {
	Stop(ctx context.Context) error
}

// stopper unifies Stopper and WithClose-marked io.Closer singletons so
// both kinds are stopped in a single reverse-ordered list.
type stopper struct {
	val any
}

func (s stopper) stop(ctx context.Context) error {
	if st, ok := s.val.(Stopper); ok {
		return st.Stop(ctx)
	}
	return s.val.(io.Closer).Close()
}

// ProvideSpec is the mutable specification that ProvideOption.Apply customizes.
// It is the open extension point for per-registration options on
// Provide/ProvideKeyed/TryProvide/TryProvideKeyed.
type ProvideSpec struct {
	// CloseOnStop registers the singleton for shutdown via io.Closer: during
	// App.Stop its Close is called in reverse construction order.
	// Without this option io.Closer singletons are never touched by the
	// app. Stopper always takes precedence over io.Closer.
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

// WithClose opts a registration into shutdown via io.Closer: App.Stop
// calls its Close in reverse construction order. Without WithClose, a
// singleton that merely implements io.Closer is ignored by the app;
// Stopper implementations are always stopped regardless.
func WithClose() ProvideOption {
	return provideOptionFunc(func(spec *ProvideSpec) { spec.CloseOnStop = true })
}

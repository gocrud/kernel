package kernel

import (
	"reflect"
	"time"
)

// decorator wraps an already-constructed instance with extra behavior. fn's
// first parameter is always the inner instance; remaining parameters are
// additional auto-wired dependencies.
type decorator struct {
	fn           reflect.Value
	hasErr       bool
	paramTypes   []reflect.Type
	paramIndices []int
}

// descriptor describes a single registration. Every descriptor is a Singleton:
// its dependency graph is resolved, cycle-checked and constructed exactly once,
// eagerly, during Build. Container only ever reads singletonVal afterwards.
type descriptor struct {
	serviceType reflect.Type
	index       int // stable position in Container.entries, assigned during Build

	isInstance bool
	instance   any

	ctor         reflect.Value // valid when !isInstance && !isProvider
	ctorHasErr   bool
	paramTypes   []reflect.Type
	paramIndices []int

	decorators []decorator

	// Provider[T] / Keyed[T] entries are auto-registered accessors. Their
	// dependency edges still participate in cycle detection and ordering, but
	// their value is created only after every normal singleton is constructed.
	isProvider      bool
	providerFactory func(*Container) any
	keyedOf         reflect.Type // set on Keyed[T] entries; edges = every keyed registration of T

	key any // registration key for keyed descriptors (diagnostics only)

	closeOnStop bool // WithClose option: shutdown via io.Closer during Container.Stop

	singletonVal any
	duration     time.Duration // construction time, for diagnostics
}

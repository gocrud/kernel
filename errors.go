package kernel

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// Sentinel errors returned (possibly wrapped) by ContainerBuilder.Build and
// Container resolution methods.
var (
	ErrServiceNotRegistered = errors.New("kernel: service not registered")
	ErrCircularDependency   = errors.New("kernel: circular dependency detected")
)

// CircularDependencyError carries the full dependency chain that formed a
// cycle, discovered during Build.
type CircularDependencyError struct {
	Chain []reflect.Type
}

func (e *CircularDependencyError) Error() string {
	names := make([]string, len(e.Chain))
	for i, t := range e.Chain {
		names[i] = t.String()
	}
	return fmt.Sprintf("%s: %s", ErrCircularDependency, strings.Join(names, " -> "))
}

func (e *CircularDependencyError) Unwrap() error {
	return ErrCircularDependency
}

// ConfigError describes a configuration loading, parsing, or binding failure.
// Source is the name of the failing source (file path, "env(...)", "flag", ...)
// or the binding section; Err is the underlying error.
type ConfigError struct {
	Source string
	Err    error
}

func (e *ConfigError) Error() string {
	if e.Source == "" {
		return e.Err.Error()
	}
	return fmt.Sprintf("kernel: config %s: %v", e.Source, e.Err)
}

func (e *ConfigError) Unwrap() error { return e.Err }

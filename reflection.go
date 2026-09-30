package kernel

import (
	"fmt"
	"reflect"
)

var errType = reflect.TypeFor[error]()

// buildDescriptor inspects factory and returns a descriptor for service type
// t. factory may be:
//   - a non-func value: used as a pre-built instance
//   - func(deps...) T or func(deps...) (T, error): an auto-wired constructor
//     where every parameter type must itself be a registered service
//
// Invalid shapes panic: they are configuration-time programming errors.
func buildDescriptor(t reflect.Type, factory any) *descriptor {
	if factory == nil {
		panic(fmt.Sprintf("kernel: nil factory registered for service %s", t))
	}

	pv := reflect.ValueOf(factory)
	if pv.Kind() != reflect.Func {
		pt := reflect.TypeOf(factory)
		if !pt.AssignableTo(t) {
			panic(fmt.Sprintf("kernel: instance of type %s is not assignable to service type %s", pt, t))
		}
		return &descriptor{serviceType: t, isInstance: true, instance: factory}
	}

	ft := pv.Type()
	hasErr := validateReturn(t, ft, "factory function")

	paramTypes := make([]reflect.Type, ft.NumIn())
	for i := range paramTypes {
		paramTypes[i] = ft.In(i)
	}
	return &descriptor{
		serviceType: t,
		ctor:        pv,
		ctorHasErr:  hasErr,
		paramTypes:  paramTypes,
	}
}

// buildDecorator inspects decoratorFn and returns a decorator for service type
// t. decoratorFn must be func(inner T, deps...) T or func(inner T, deps...)
// (T, error).
func buildDecorator(t reflect.Type, decoratorFn any) decorator {
	pv := reflect.ValueOf(decoratorFn)
	if pv.Kind() != reflect.Func {
		panic(fmt.Sprintf("kernel: decorator for %s must be a function", t))
	}
	ft := pv.Type()
	if ft.NumIn() < 1 || ft.In(0) != t {
		panic(fmt.Sprintf("kernel: decorator for %s must take %s as its first parameter", t, t))
	}
	hasErr := validateReturn(t, ft, "decorator function")

	paramTypes := make([]reflect.Type, ft.NumIn()-1)
	for i := range paramTypes {
		paramTypes[i] = ft.In(i + 1)
	}
	return decorator{fn: pv, hasErr: hasErr, paramTypes: paramTypes}
}

// validateReturn checks that ft returns (T) or (T, error) for the given service
// type, panicking with a description if it doesn't.
func validateReturn(serviceType, ft reflect.Type, what string) (hasErr bool) {
	if ft.NumOut() != 1 && ft.NumOut() != 2 {
		panic(fmt.Sprintf("kernel: %s for %s must return (T) or (T, error), got %s", what, serviceType, ft))
	}
	if !ft.Out(0).AssignableTo(serviceType) {
		panic(fmt.Sprintf("kernel: %s return type %s is not assignable to service type %s", what, ft.Out(0), serviceType))
	}
	hasErr = ft.NumOut() == 2
	if hasErr && !ft.Out(1).Implements(errType) {
		panic(fmt.Sprintf("kernel: %s for %s second return value must be error, got %s", what, serviceType, ft.Out(1)))
	}
	return hasErr
}

// finishCall extracts the value from a constructor/decorator call result,
// returning the non-nil error when one was returned.
func finishCall(out []reflect.Value, hasErr bool) (any, error) {
	if hasErr && !out[1].IsNil() {
		return nil, out[1].Interface().(error)
	}
	return out[0].Interface(), nil
}

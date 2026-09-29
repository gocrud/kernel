package kernel

import (
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"time"
)

// graphBuilder accumulates the frozen, indexed view of a ContainerBuilder while
// Build constructs it.
type graphBuilder struct {
	entries []*descriptor

	typeIndex   map[reflect.Type]int
	typeIndices map[reflect.Type][]int

	keyedIndex   map[reflect.Type]map[any]int
	keyedIndices map[reflect.Type]map[any][]int

	watchers []*reloadWatcher
}

func (g *graphBuilder) appendEntry(d *descriptor) int {
	idx := len(g.entries)
	d.index = idx
	g.entries = append(g.entries, d)
	return idx
}

// resolveParamIndices resolves each of paramTypes to the index of its last
// unkeyed registration, building the static dependency edges used for cycle
// detection and construction ordering.
func (g *graphBuilder) resolveParamIndices(forType reflect.Type, paramTypes []reflect.Type) ([]int, error) {
	indices := make([]int, len(paramTypes))
	for i, pt := range paramTypes {
		idx, ok := g.typeIndex[pt]
		if !ok {
			return nil, fmt.Errorf("kernel: resolving dependency %s for %s: %w", pt, forType, ErrServiceNotRegistered)
		}
		indices[i] = idx
	}
	return indices, nil
}

// topoSort computes a construction order for g.entries (dependencies before
// dependents) via DFS, returning a CircularDependencyError if the dependency
// graph contains a cycle.
func (g *graphBuilder) topoSort() ([]int, error) {
	const (
		white = iota
		gray
		black
	)
	color := make([]int, len(g.entries))
	order := make([]int, 0, len(g.entries))
	var path []reflect.Type

	var visit func(idx int) error
	visit = func(idx int) error {
		switch color[idx] {
		case black:
			return nil
		case gray:
			chain := append(append([]reflect.Type{}, path...), g.entries[idx].serviceType)
			return &CircularDependencyError{Chain: chain}
		}
		color[idx] = gray
		path = append(path, g.entries[idx].serviceType)

		d := g.entries[idx]
		deps := append([]int{}, d.paramIndices...)
		for _, dec := range d.decorators {
			deps = append(deps, dec.paramIndices...)
		}
		for _, depIdx := range deps {
			if err := visit(depIdx); err != nil {
				return err
			}
		}

		path = path[:len(path)-1]
		color[idx] = black
		order = append(order, idx)
		return nil
	}

	for idx := range g.entries {
		if err := visit(idx); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// Build resolves every registration's dependency graph, checks for missing and
// circular dependencies, and eagerly constructs every service in dependency
// order. Configuration bindings are loaded and bound here; a returned error
// means nothing was partially constructed in a usable way.
func (b *ContainerBuilder) Build() (*Container, error) {
	g := &graphBuilder{
		typeIndex:    make(map[reflect.Type]int),
		typeIndices:  make(map[reflect.Type][]int),
		keyedIndex:   make(map[reflect.Type]map[any]int),
		keyedIndices: make(map[reflect.Type]map[any][]int),
	}

	for t, descs := range b.descriptors {
		for _, d := range descs {
			idx := g.appendEntry(d)
			g.typeIndex[t] = idx
			g.typeIndices[t] = append(g.typeIndices[t], idx)
		}
	}
	for t, byKey := range b.keyedDescriptors {
		for key, descs := range byKey {
			idxs := make([]int, 0, len(descs))
			for _, d := range descs {
				idxs = append(idxs, g.appendEntry(d))
			}
			if g.keyedIndex[t] == nil {
				g.keyedIndex[t] = make(map[any]int)
				g.keyedIndices[t] = make(map[any][]int)
			}
			g.keyedIndex[t][key] = idxs[len(idxs)-1]
			g.keyedIndices[t][key] = idxs
		}
	}

	// Configuration bindings become instance descriptors; reloadable bindings
	// additionally produce an OptionsMonitor and a watcher.
	for _, cb := range b.configBindings {
		opts, err := cb.buildOpts()
		if err != nil {
			return nil, err
		}
		od := &descriptor{serviceType: cb.optsType, isInstance: true, instance: opts}
		idx := g.appendEntry(od)
		g.typeIndex[cb.optsType] = idx
		g.typeIndices[cb.optsType] = append(g.typeIndices[cb.optsType], idx)
		if cb.reloadable {
			mon, err := cb.buildMonitor()
			if err != nil {
				return nil, err
			}
			cb.mon = mon
			md := &descriptor{serviceType: cb.monType, isInstance: true, instance: mon}
			midx := g.appendEntry(md)
			g.typeIndex[cb.monType] = midx
			g.typeIndices[cb.monType] = append(g.typeIndices[cb.monType], midx)
			g.watchers = append(g.watchers, newReloadWatcher(cb, b.loggerOrDefault()))
		}
	}

	// *slog.Logger is auto-registered for injection unless the user provided
	// their own registration.
	slogT := reflect.TypeFor[*slog.Logger]()
	if !b.isRegistered(slogT) {
		idx := g.appendEntry(&descriptor{serviceType: slogT, isInstance: true, instance: b.loggerOrDefault()})
		g.typeIndex[slogT] = idx
		g.typeIndices[slogT] = append(g.typeIndices[slogT], idx)
	}

	// Resolve static dependency indices.
	for _, d := range g.entries {
		switch {
		case d.isInstance:
			// nothing to resolve
		case d.isProvider:
			if d.keyedOf != nil {
				for _, idxs := range g.keyedIndices[d.keyedOf] {
					d.paramIndices = append(d.paramIndices, idxs...)
				}
			} else {
				idxs, err := g.resolveParamIndices(d.serviceType, d.paramTypes)
				if err != nil {
					return nil, err
				}
				d.paramIndices = idxs
			}
		default:
			idxs, err := g.resolveParamIndices(d.serviceType, d.paramTypes)
			if err != nil {
				return nil, err
			}
			d.paramIndices = idxs
			for i := range d.decorators {
				decIdxs, err := g.resolveParamIndices(d.serviceType, d.decorators[i].paramTypes)
				if err != nil {
					return nil, err
				}
				d.decorators[i].paramIndices = decIdxs
			}
		}
	}

	order, err := g.topoSort()
	if err != nil {
		return nil, err
	}

	c := &Container{
		entries:      g.entries,
		typeIndex:    g.typeIndex,
		typeIndices:  g.typeIndices,
		keyedIndex:   g.keyedIndex,
		keyedIndices: g.keyedIndices,
		watchers:     g.watchers,
		logger:       b.loggerOrDefault(),
	}

	for _, idx := range order {
		d := g.entries[idx]
		if d.isProvider {
			// Provider / Keyed accessors are created in topological order; their
			// dependency edges guarantee every target singleton is constructed
			// first, so the first Get call is always a plain lookup.
			d.singletonVal = d.providerFactory(c)
			continue
		}
		if d.isInstance {
			d.singletonVal = d.instance
		} else {
			args := make([]reflect.Value, len(d.paramIndices))
			for i, pidx := range d.paramIndices {
				args[i] = reflect.ValueOf(g.entries[pidx].singletonVal)
			}
			start := time.Now()
			v, err := finishCall(d.ctor.Call(args), d.ctorHasErr)
			d.duration = time.Since(start)
			if err != nil {
				return nil, fmt.Errorf("kernel: constructing %s: %w", d.serviceType, err)
			}
			d.singletonVal = v
		}
		for _, dec := range d.decorators {
			args := make([]reflect.Value, len(dec.paramIndices)+1)
			args[0] = reflect.ValueOf(d.singletonVal)
			for i, pidx := range dec.paramIndices {
				args[i+1] = reflect.ValueOf(g.entries[pidx].singletonVal)
			}
			v, err := finishCall(dec.fn.Call(args), dec.hasErr)
			if err != nil {
				return nil, fmt.Errorf("kernel: decorating %s: %w", d.serviceType, err)
			}
			d.singletonVal = v
		}
		registerLifecycle(c, d)
	}

	for _, w := range g.watchers {
		w.start()
	}
	return c, nil
}

func registerLifecycle(c *Container, d *descriptor) {
	v := d.singletonVal
	if s, ok := v.(Startable); ok {
		c.startables = append(c.startables, s)
	}
	if _, ok := v.(Stoppable); ok {
		c.stoppables = append(c.stoppables, stoppable{val: v})
		return
	}
	if d.closeOnStop {
		if _, ok := v.(io.Closer); ok {
			c.stoppables = append(c.stoppables, stoppable{val: v})
		}
	}
}

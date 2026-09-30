package kernel

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// Describe writes a human-readable tree of every registered service: its form
// (instance / constructor / provider / keyed accessor), keyed registrations,
// decorators, dependency edges and construction duration.
func (app *App) Describe(w io.Writer) {
	fmt.Fprintln(w, "kernel app:")
	for _, d := range app.entries {
		kind := "instance"
		switch {
		case d.isAccessor && d.keyedOf != nil:
			kind = "keyed-accessor"
		case d.isAccessor:
			kind = "provider"
		case d.isInstance:
			kind = "instance"
		case len(d.decorators) > 0:
			kind = fmt.Sprintf("constructor (+%d decorators)", len(d.decorators))
		default:
			kind = "constructor"
		}
		line := fmt.Sprintf("  %-46s %s", d.serviceType.String(), kind)
		if d.key != nil {
			line += fmt.Sprintf("  [key=%v]", d.key)
		}
		if d.duration > 0 {
			line += fmt.Sprintf("  %v", d.duration.Round(time.Microsecond))
		}
		if len(d.paramIndices) > 0 {
			deps := make([]string, 0, len(d.paramIndices))
			for _, pidx := range d.paramIndices {
				deps = append(deps, app.entries[pidx].serviceType.String())
			}
			line += "  <- " + strings.Join(deps, ", ")
		}
		fmt.Fprintln(w, line)
	}
}

// Dot writes the dependency graph in Graphviz DOT format.
func (app *App) Dot(w io.Writer) {
	fmt.Fprintln(w, "digraph kernel {")
	fmt.Fprintln(w, "  rankdir=LR;")
	for i, d := range app.entries {
		name := d.serviceType.String()
		if d.key != nil {
			name = fmt.Sprintf("%s\\n[key=%v]", name, d.key)
		}
		fmt.Fprintf(w, "  n%d [label=%q];\n", i, name)
	}
	for i, d := range app.entries {
		for _, pidx := range d.paramIndices {
			fmt.Fprintf(w, "  n%d -> n%d;\n", pidx, i)
		}
	}
	fmt.Fprintln(w, "}")
}

package config

import (
	"flag"
	"os"
	"strings"

	"github.com/gocrud/kernel"
)

// Compile-time assertions.
var (
	_ kernel.Source = (*fileSource)(nil)
	_ kernel.Source = (*mapSource)(nil)
	_ kernel.Source = (*envSource)(nil)
	_ kernel.Source = (*flagSource)(nil)
)

// fileSource reads a configuration file, choosing the codec by extension.
type fileSource struct {
	path string
}

func (s *fileSource) Name() string                  { return s.path }
func (s *fileSource) Load() (map[string]any, error) { return decodeFile(s.path) }

// mapSource serves an in-memory map.
type mapSource struct {
	m    map[string]any
	name string
}

func (s *mapSource) Name() string { return s.name }
func (s *mapSource) Load() (map[string]any, error) {
	return s.m, nil
}

// envSource maps environment variables to dotted keys: every variable whose
// name starts with prefix contributes a key built from the full lower-cased
// name with "_" replaced by ".", so APP_PORT becomes app.port. The prefix only
// filters which variables are included and does not affect the key.
type envSource struct {
	prefix string
}

func (s *envSource) Name() string { return "env(" + s.prefix + ")" }
func (s *envSource) Load() (map[string]any, error) {
	out := make(map[string]any)
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if s.prefix != "" && !strings.HasPrefix(k, s.prefix) {
			continue
		}
		key := strings.ToLower(strings.ReplaceAll(k, "_", "."))
		out[key] = v
	}
	return out, nil
}

// flagSource maps set command-line flags to keys, using the flag name verbatim
// (dots are allowed in flag names).
type flagSource struct {
	fs *flag.FlagSet
}

func (s *flagSource) Name() string { return "flag" }
func (s *flagSource) Load() (map[string]any, error) {
	out := make(map[string]any)
	if s.fs == nil {
		return out, nil
	}
	s.fs.Visit(func(f *flag.Flag) {
		out[f.Name] = f.Value.String()
	})
	return out, nil
}

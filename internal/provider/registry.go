package provider

import (
	"fmt"
	"sort"
)

// Factory builds a provider for a target. The target string is provider-defined: for
// Docker, the daemon address; empty means the local default.
type Factory func(target string) (Provider, error)

// Registry maps provider names to factories. The agent and the conformance runner each
// build one with the providers compiled into the binary.
type Registry struct {
	factories map[string]Factory
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{factories: map[string]Factory{}} }

// Add registers a provider by the name its Describe reports.
func (r *Registry) Add(name string, f Factory) error {
	if name == "" || f == nil {
		return fmt.Errorf("provider registry: name and factory are required")
	}
	if _, ok := r.factories[name]; ok {
		return fmt.Errorf("provider registry: %q is already registered", name)
	}
	r.factories[name] = f
	return nil
}

// New builds the named provider for target.
func (r *Registry) New(name, target string) (Provider, error) {
	f, ok := r.factories[name]
	if !ok {
		return nil, fmt.Errorf("provider %q is not in this build (have %v)", name, r.Names())
	}
	p, err := f(target)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", name, err)
	}
	return p, nil
}

// Names lists the registered providers.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.factories))
	for n := range r.factories {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

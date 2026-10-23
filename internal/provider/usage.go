package provider

import "context"

// Usage is one workload's current resource use (ADR-0040).
type Usage struct {
	CPUMillicores    int64
	MemoryBytes      int64
	MemoryLimitBytes int64
}

// UsageReader is implemented by providers that can sample their workloads' resource
// use cheaply. It is optional, like ImageStore: without it, workloads report none.
type UsageReader interface {
	// Usage samples every workload the provider runs, keyed by its ResourceRef
	// (Environment, App, Slot; slugs empty).
	Usage(ctx context.Context) (map[ResourceRef]Usage, error)
}

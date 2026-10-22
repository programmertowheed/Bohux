//go:build !linux

package swarm

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// probeInNamespace needs Linux network namespaces; Swarm nodes run Linux.
func probeInNamespace(context.Context, *http.Client, int, int32, string, time.Duration) error {
	return errors.New("health checks need Linux network namespaces")
}

package docker

import (
	"os"

	"gopkg.in/yaml.v3"

	"github.com/bohumix/bohux/internal/provider"
)

// The functions below are this provider's Docker mechanics that the Swarm provider
// shares (ADR-0045): it drives the same docker CLI against the same daemon.

// CPULimit turns a Kubernetes CPU quantity into Compose's cpus.
func CPULimit(q string) (string, error) { return cpuLimit(q) }

// MemoryLimit turns a Kubernetes memory quantity into Compose's bytes notation.
func MemoryLimit(q string) (string, error) { return memoryLimit(q) }

// PullEnv returns the environment that gives one docker command a throwaway registry
// credential, and a function that removes it (ADR-0041).
func PullEnv(src provider.PullConfigSource) ([]string, func(), error) { return pullEnv(src) }

// WriteAtomic writes a file through a temporary file and a rename, with mode.
func WriteAtomic(path string, data []byte, mode os.FileMode) error {
	return writeAtomic(path, data, mode)
}

// SetReplicas sets services.app.deploy.replicas in a parsed Compose or stack file,
// keeping its comments and everything else as written.
func SetReplicas(doc *yaml.Node, replicas int32) bool { return setReplicas(doc, replicas) }

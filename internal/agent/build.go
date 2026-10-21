package agent

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
)

// Build failure reasons (BuildResult.failure.reason). Stable across releases.
const (
	BuildDiskPressure = "DISK_PRESSURE"
	BuildInvalid      = "BUILD_REQUEST_INVALID"
	BuildFetchFailed  = "SOURCE_FETCH_FAILED"
	BuildFailed       = "BUILD_FAILED"
	BuildNoBuilder    = "BUILDER_UNAVAILABLE"
)

// BuilderName is the buildx builder the agent builds with. An operator may create it
// beforehand with their own options (proxies, mirrors, limits); the agent then uses it
// as it is (ADR-0028).
const BuilderName = "bohux"

// Builder defaults for the builder the agent creates (ADR-0011, ADR-0020 item 3).
const (
	defaultBuildMemory   = "1g"
	defaultBuildCPUQuota = "100000" // one CPU per 100 ms period
	defaultCacheKeep     = 10_000_000_000
	keptResults          = 32
)

var imageID = regexp.MustCompile(`sha256:[a-f0-9]{64}`)

// DiskState reports disk pressure; DiskMonitor implements it.
type DiskState interface {
	Degraded() (bool, *bohuxv1.ErrorDetail)
}

// Builder builds Releases from Git on this node with BuildKit through buildx and loads
// the image into the local daemon (ADR-0028). One build runs at a time.
type Builder struct {
	// Disk, if set, refuses builds while the node is under disk pressure (ADR-0020
	// item 4).
	Disk DiskState
	// WorkDir holds checkouts while they build, and the builder's config.
	WorkDir string
	// Env is added to the environment of git and docker; DOCKER_HOST selects the daemon.
	Env []string

	mu      sync.Mutex
	results map[string]*agentv1.BuildResult
	order   []string
}

// Build runs one build to the end and returns its result; it never returns nil. A
// Release built recently returns its earlier success without building again.
func (b *Builder) Build(ctx context.Context, req *agentv1.BuildRequest) *agentv1.BuildResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r, ok := b.results[req.GetReleaseId()]; ok {
		return r
	}
	start := time.Now()
	res := b.build(ctx, req)
	log := slog.With("release", req.GetReleaseId(), "app", req.GetAppId(), "took", time.Since(start).Round(time.Millisecond))
	if res.GetFailure() != nil {
		log.WarnContext(ctx, "build failed", "reason", res.GetFailure().GetReason())
		return res
	}
	log.InfoContext(ctx, "build done", "image", res.GetImageId(), "commit", res.GetCommit())
	b.remember(res)
	return res
}

func (b *Builder) build(ctx context.Context, req *agentv1.BuildRequest) *agentv1.BuildResult {
	fail := func(reason string) *agentv1.BuildResult {
		return &agentv1.BuildResult{ReleaseId: req.GetReleaseId(), Failure: &bohuxv1.ErrorDetail{Reason: reason}}
	}
	if b.Disk != nil {
		if degraded, _ := b.Disk.Degraded(); degraded {
			return fail(BuildDiskPressure)
		}
	}
	sub := filepath.Clean("/" + req.GetSubpath())
	if req.GetReleaseId() == "" || req.GetAppId() == "" || req.GetRepoUrl() == "" ||
		strings.HasPrefix(req.GetRepoUrl(), "-") || strings.HasPrefix(req.GetRef(), "-") ||
		strings.Contains(req.GetSubpath(), "..") {
		return fail(BuildInvalid)
	}
	if err := os.MkdirAll(b.WorkDir, 0o700); err != nil {
		slog.ErrorContext(ctx, "build work dir", "err", err)
		return fail(BuildFailed)
	}
	dir, err := os.MkdirTemp(b.WorkDir, "build-")
	if err != nil {
		slog.ErrorContext(ctx, "build checkout dir", "err", err)
		return fail(BuildFailed)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	commit, err := b.fetch(ctx, dir, req.GetRepoUrl(), req.GetRef())
	if err != nil {
		slog.WarnContext(ctx, "fetch build source", "release", req.GetReleaseId(), "err", err)
		return fail(BuildFetchFailed)
	}
	if err := b.ensureBuilder(ctx); err != nil {
		slog.ErrorContext(ctx, "buildx builder", "err", err)
		return fail(BuildNoBuilder)
	}
	out, err := b.run(ctx, "docker", "buildx", "build", "--builder", BuilderName, "--load", "-q",
		"--label", "io.bohux.app="+req.GetAppId(), "--label", "io.bohux.release="+req.GetReleaseId(),
		filepath.Join(dir, sub))
	if err != nil {
		slog.WarnContext(ctx, "docker buildx build", "release", req.GetReleaseId(), "err", err)
		return fail(BuildFailed)
	}
	id := imageID.FindString(string(out))
	if id == "" {
		slog.WarnContext(ctx, "docker buildx build printed no image ID", "release", req.GetReleaseId())
		return fail(BuildFailed)
	}
	return &agentv1.BuildResult{ReleaseId: req.GetReleaseId(), ImageId: id, Commit: commit}
}

// fetch checks out ref (branch, tag, or commit) of repo into dir and returns the
// commit it resolved to. A shallow fetch of the one ref is all a build needs.
func (b *Builder) fetch(ctx context.Context, dir, repo, ref string) (string, error) {
	if ref == "" {
		ref = "HEAD"
	}
	steps := [][]string{
		{"git", "init", "-q", dir},
		{"git", "-C", dir, "fetch", "-q", "--depth", "1", "--", repo, ref},
		{"git", "-C", dir, "checkout", "-q", "FETCH_HEAD"},
	}
	for _, s := range steps {
		if _, err := b.run(ctx, s[0], s[1:]...); err != nil {
			return "", err
		}
	}
	out, err := b.run(ctx, "git", "-C", dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ensureBuilder creates the bohux builder with limits and a capped cache unless it
// already exists.
func (b *Builder) ensureBuilder(ctx context.Context) error {
	if _, err := b.run(ctx, "docker", "buildx", "inspect", BuilderName); err == nil {
		return nil
	}
	cfg := filepath.Join(b.WorkDir, "buildkitd.toml")
	conf := fmt.Sprintf("[worker.oci]\n  gc = true\n  gckeepstorage = %d\n", defaultCacheKeep)
	if err := os.WriteFile(cfg, []byte(conf), 0o600); err != nil {
		return fmt.Errorf("write buildkitd config: %w", err)
	}
	_, err := b.run(ctx, "docker", "buildx", "create", "--name", BuilderName,
		"--driver", "docker-container", "--driver-opt", "memory="+defaultBuildMemory,
		"--driver-opt", "cpu-quota="+defaultBuildCPUQuota, "--buildkitd-config", cfg)
	return err
}

// run executes a command and returns its stdout. Its error carries the exit status
// and the last lines of stderr; builds take no secret inputs in v0.1 (ADR-0028).
func (b *Builder) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), b.Env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", name, args[0], err, tail(stderr.String(), 5))
	}
	return stdout.Bytes(), nil
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

func (b *Builder) remember(r *agentv1.BuildResult) {
	if b.results == nil {
		b.results = map[string]*agentv1.BuildResult{}
	}
	b.results[r.GetReleaseId()] = r
	b.order = append(b.order, r.GetReleaseId())
	if len(b.order) > keptResults {
		delete(b.results, b.order[0])
		b.order = b.order[1:]
	}
}

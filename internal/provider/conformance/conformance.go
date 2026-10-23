// Package conformance is the provider conformance suite (docs/specs/provider-interface.md,
// "Conformance"): the gate every provider passes before it merges (CLAUDE.md rule 4). It
// runs the full lifecycle against a real target through the Provider interface, and
// looks at the target directly through the provider's Inspector.
//
// Run it with: go test ./providers/_conformance -provider=<name> -target=<target>
package conformance

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bohumix/bohux/internal/provider"
)

// Inspector looks at the target without going through the Provider, so the suite can
// check what the provider claims.
type Inspector interface {
	// Leftovers lists every target object belonging to ref: containers, networks,
	// volumes, files. It must be empty after Destroy.
	Leftovers(ctx context.Context, ref provider.ResourceRef) ([]string, error)
	// Instances counts running instances of ref.
	Instances(ctx context.Context, ref provider.ResourceRef) (int, error)
	// Artifacts returns the readable artifacts Apply wrote for ref (rule 2), by path.
	Artifacts(ctx context.Context, ref provider.ResourceRef) (map[string][]byte, error)
	// SecretFiles returns the target's declared secret files for ref, by path.
	SecretFiles(ctx context.Context, ref provider.ResourceRef) (map[string][]byte, error)
	// Reach connects to endpoint from where the node's ingress runs, and fails if
	// nothing accepts the connection.
	Reach(ctx context.Context, endpoint string) error
}

// Subject is a provider under test.
type Subject struct {
	Provider  provider.Provider
	Inspector Inspector
	// Fixture returns the spec for a release of a small workload that logs
	// LogMarker(release) when it starts and serves its health check. It must use ref
	// unchanged. The suite fills in Secrets and Hash.
	Fixture   func(ref provider.ResourceRef, release int, replicas int32) provider.Spec
	LogMarker func(release int) string
	// Timeout bounds each wait for convergence; zero means two minutes.
	Timeout time.Duration
}

// SecretKey is the environment variable the suite's secret is delivered as.
const SecretKey = "BOHUX_CONFORMANCE_SECRET"

// Case is one conformance step. Cases run in order on one workload and build on
// each other's state.
type Case struct {
	Name string
	Run  func(ctx context.Context, h *Harness) error
}

// Harness carries the state the cases share.
type Harness struct {
	S   Subject
	Ref provider.ResourceRef

	secret      string
	release     int
	replicas    int32
	baseline    int // leftover count once the workload first converged
	secretsSeen []string
	ingress     bool
}

// NewHarness picks a fresh workload name, so runs do not collide.
func NewHarness(s Subject) *Harness {
	if s.Timeout == 0 {
		s.Timeout = 2 * time.Minute
	}
	app := "c" + randomHex(4)
	return &Harness{S: s, Ref: provider.ResourceRef{Environment: "conformance", App: app,
		EnvironmentSlug: "conformance", AppSlug: app}, replicas: 1}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type secrets map[string]string

func (s secrets) Open() (map[string]string, error) { return s, nil }

// spec builds the desired state for a release with the current secret and replicas.
func (h *Harness) spec(release int) provider.Spec {
	sp := h.S.Fixture(h.Ref, release, h.replicas)
	sp.Secrets = secrets{SecretKey: h.secret}
	sp.Ingress = h.ingress
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%s|%t", sp.Image, release, h.replicas, h.secret, h.ingress)))
	sp.Hash = hex.EncodeToString(sum[:])
	return sp
}

func (h *Harness) apply(ctx context.Context, release int) (provider.Spec, provider.ApplyResult, error) {
	sp := h.spec(release)
	res, err := h.S.Provider.Apply(ctx, provider.ApplyRequest{Spec: sp})
	if err != nil {
		return sp, res, fmt.Errorf("apply release %d: %w", release, err)
	}
	return sp, res, nil
}

// converged waits until Status reports hash, running, healthy, and the expected
// number of ready replicas, and the target holds exactly that many instances.
func (h *Harness) converged(ctx context.Context, hash string) error {
	deadline := time.Now().Add(h.S.Timeout)
	var last provider.Status
	var instances int
	for {
		st, err := h.S.Provider.Status(ctx, h.Ref)
		if err != nil {
			return fmt.Errorf("status: %w", err)
		}
		n, err := h.S.Inspector.Instances(ctx, h.Ref)
		if err != nil {
			return fmt.Errorf("count instances: %w", err)
		}
		last, instances = st, n
		if st.Exists && st.Hash == hash && st.Running && st.Healthy &&
			st.ReadyReplicas == h.replicas && n == int(h.replicas) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not converged to %.12s with %d replicas: status %+v, %d instances",
				hash, h.replicas, last, instances)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (h *Harness) leftovers(ctx context.Context) ([]string, error) {
	l, err := h.S.Inspector.Leftovers(ctx, h.Ref)
	if err != nil {
		return nil, fmt.Errorf("list leftovers: %w", err)
	}
	sort.Strings(l)
	return l, nil
}

func (h *Harness) exec(ctx context.Context, cmd ...string) (string, int, error) {
	s, err := h.S.Provider.Exec(ctx, provider.ExecRequest{Ref: h.Ref, Command: cmd})
	if err != nil {
		return "", 0, fmt.Errorf("exec %v: %w", cmd, err)
	}
	defer func() { _ = s.Close() }()
	_ = s.Stdin().Close()
	out, err := io.ReadAll(s.Stdout())
	if err != nil {
		return "", 0, fmt.Errorf("read exec output: %w", err)
	}
	code, err := s.Wait()
	if err != nil {
		return "", 0, fmt.Errorf("wait for exec: %w", err)
	}
	return string(out), code, nil
}

// logs collects non-following log output.
func (h *Harness) logs(ctx context.Context) (string, error) {
	seq, err := h.S.Provider.Logs(ctx, provider.LogRequest{Ref: h.Ref})
	if err != nil {
		return "", fmt.Errorf("logs: %w", err)
	}
	var b strings.Builder
	for line, err := range seq {
		if err != nil {
			return "", fmt.Errorf("read logs: %w", err)
		}
		b.WriteString(line.Message)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// noSecretIn fails if any value the suite has used appears in the given content.
func (h *Harness) noSecretIn(what string, contents map[string][]byte) error {
	for path, c := range contents {
		for _, s := range h.secretsSeen {
			if bytes.Contains(c, []byte(s)) {
				return fmt.Errorf("secret value appears in %s %s", what, path)
			}
		}
	}
	return nil
}

func (h *Harness) newSecret() {
	h.secret = "s3cr3t-" + randomHex(8)
	h.secretsSeen = append(h.secretsSeen, h.secret)
}

// Cases returns the suite, in order.
func Cases() []Case {
	return []Case{
		{"describe declares every verb", func(ctx context.Context, h *Harness) error {
			d, err := h.S.Provider.Describe(ctx)
			if err != nil {
				return fmt.Errorf("describe: %w", err)
			}
			if d.Name == "" || d.Version == "" {
				return errors.New("describe: name and version are required")
			}
			have := map[provider.Verb]bool{}
			for _, v := range d.Capabilities {
				have[v] = true
			}
			for _, v := range provider.AllVerbs {
				if !have[v] {
					return fmt.Errorf("verb %q not declared; a gap must be declared and return ErrUnsupported, "+
						"and this suite requires every verb", v)
				}
			}
			return nil
		}},
		{"plan does not mutate", func(ctx context.Context, h *Harness) error {
			h.newSecret()
			p, err := h.S.Provider.Plan(ctx, provider.PlanRequest{Spec: h.spec(1)})
			if err != nil {
				return fmt.Errorf("plan: %w", err)
			}
			if len(p.Changes) == 0 {
				return errors.New("plan from empty lists no changes")
			}
			l, err := h.leftovers(ctx)
			if err != nil {
				return err
			}
			if len(l) != 0 {
				return fmt.Errorf("plan created %v", l)
			}
			return nil
		}},
		{"apply from empty becomes healthy", func(ctx context.Context, h *Harness) error {
			sp, _, err := h.apply(ctx, 1)
			if err != nil {
				return err
			}
			h.release = 1
			if err := h.converged(ctx, sp.Hash); err != nil {
				return err
			}
			arts, err := h.S.Inspector.Artifacts(ctx, h.Ref)
			if err != nil {
				return fmt.Errorf("read artifacts: %w", err)
			}
			if len(arts) == 0 {
				return errors.New("apply wrote no readable artifact (rule 2)")
			}
			l, err := h.leftovers(ctx)
			if err != nil {
				return err
			}
			h.baseline = len(l)
			return nil
		}},
		{"unchanged re-apply is a no-op", func(ctx context.Context, h *Harness) error {
			before, err := h.leftovers(ctx)
			if err != nil {
				return err
			}
			p, err := h.S.Provider.Plan(ctx, provider.PlanRequest{Spec: h.spec(h.release)})
			if err != nil {
				return fmt.Errorf("plan: %w", err)
			}
			if len(p.Changes) != 0 {
				return fmt.Errorf("plan of the applied spec lists changes: %v", p.Changes)
			}
			_, res, err := h.apply(ctx, h.release)
			if err != nil {
				return err
			}
			if res.Changed {
				return errors.New("re-applying an unchanged spec reported a change")
			}
			after, err := h.leftovers(ctx)
			if err != nil {
				return err
			}
			if strings.Join(before, ",") != strings.Join(after, ",") {
				return fmt.Errorf("unchanged re-apply churned the target: %v -> %v", before, after)
			}
			return nil
		}},
		{"secrets only in the secret file", func(ctx context.Context, h *Harness) error {
			arts, err := h.S.Inspector.Artifacts(ctx, h.Ref)
			if err != nil {
				return fmt.Errorf("read artifacts: %w", err)
			}
			if err := h.noSecretIn("artifact", arts); err != nil {
				return err
			}
			files, err := h.S.Inspector.SecretFiles(ctx, h.Ref)
			if err != nil {
				return fmt.Errorf("read secret files: %w", err)
			}
			found := false
			for _, c := range files {
				found = found || bytes.Contains(c, []byte(h.secret))
			}
			if !found {
				return errors.New("secret value is not in the declared secret file")
			}
			out, code, err := h.exec(ctx, "printenv", SecretKey)
			if err != nil {
				return err
			}
			if code != 0 || strings.TrimSpace(out) != h.secret {
				return fmt.Errorf("workload does not see its secret (exit %d)", code)
			}
			logs, err := h.logs(ctx)
			if err != nil {
				return err
			}
			return h.noSecretIn("logs", map[string][]byte{"": []byte(logs)})
		}},
		{"secret rotation re-renders and restarts", func(ctx context.Context, h *Harness) error {
			h.newSecret()
			sp, _, err := h.apply(ctx, h.release)
			if err != nil {
				return err
			}
			if err := h.converged(ctx, sp.Hash); err != nil {
				return err
			}
			out, _, err := h.exec(ctx, "printenv", SecretKey)
			if err != nil {
				return err
			}
			if strings.TrimSpace(out) != h.secret {
				return errors.New("workload still sees the old secret after rotation")
			}
			files, err := h.S.Inspector.SecretFiles(ctx, h.Ref)
			if err != nil {
				return fmt.Errorf("read secret files: %w", err)
			}
			for path, c := range files {
				for _, old := range h.secretsSeen[:len(h.secretsSeen)-1] {
					if bytes.Contains(c, []byte(old)) {
						return fmt.Errorf("old secret value still in %s", path)
					}
				}
			}
			return nil
		}},
		{"a changed spec converges", func(ctx context.Context, h *Harness) error {
			sp, _, err := h.apply(ctx, 2)
			if err != nil {
				return err
			}
			h.release = 2
			return h.converged(ctx, sp.Hash)
		}},
		{"an interrupted apply converges without duplicates", func(ctx context.Context, h *Harness) error {
			for i, d := range []time.Duration{time.Millisecond, 50 * time.Millisecond, 500 * time.Millisecond} {
				cut, cancel := context.WithTimeout(ctx, d)
				_, _, _ = h.apply(cut, 3+i%2) // may fail: that is the point
				cancel()
				sp, _, err := h.apply(ctx, 3+i%2)
				if err != nil {
					return fmt.Errorf("re-apply after an interruption at %v: %w", d, err)
				}
				h.release = 3 + i%2
				if err := h.converged(ctx, sp.Hash); err != nil {
					return fmt.Errorf("after an interruption at %v: %w", d, err)
				}
				l, err := h.leftovers(ctx)
				if err != nil {
					return err
				}
				if len(l) != h.baseline {
					return fmt.Errorf("after an interruption at %v: %d target objects, want %d: %v",
						d, len(l), h.baseline, l)
				}
			}
			return nil
		}},
		{"rollback is applying the previous release again", func(ctx context.Context, h *Harness) error {
			sp, _, err := h.apply(ctx, 1)
			if err != nil {
				return err
			}
			h.release = 1
			if err := h.converged(ctx, sp.Hash); err != nil {
				return err
			}
			l, err := h.leftovers(ctx)
			if err != nil {
				return err
			}
			if len(l) != h.baseline {
				return fmt.Errorf("rollback left %d target objects, want %d (orphans): %v", len(l), h.baseline, l)
			}
			return nil
		}},
		{"scale changes the replica count", func(ctx context.Context, h *Harness) error {
			for _, n := range []int32{2, 1} {
				if err := h.S.Provider.Scale(ctx, provider.ScaleRequest{Ref: h.Ref, Replicas: n}); err != nil {
					return fmt.Errorf("scale to %d: %w", n, err)
				}
				h.replicas = n
				deadline := time.Now().Add(h.S.Timeout)
				for {
					st, err := h.S.Provider.Status(ctx, h.Ref)
					if err != nil {
						return fmt.Errorf("status: %w", err)
					}
					got, err := h.S.Inspector.Instances(ctx, h.Ref)
					if err != nil {
						return fmt.Errorf("count instances: %w", err)
					}
					if st.Running && st.ReadyReplicas == n && got == int(n) {
						break
					}
					if time.Now().After(deadline) {
						return fmt.Errorf("scale to %d: status %+v, %d instances", n, st, got)
					}
					time.Sleep(200 * time.Millisecond)
				}
			}
			// Re-apply the same release at the original count: scaling must not have
			// left the target drifted from its spec.
			sp, _, err := h.apply(ctx, h.release)
			if err != nil {
				return err
			}
			return h.converged(ctx, sp.Hash)
		}},
		{"ingress endpoints are reported and reachable", func(ctx context.Context, h *Harness) error {
			h.ingress = true
			sp, _, err := h.apply(ctx, h.release)
			if err != nil {
				return err
			}
			if err := h.converged(ctx, sp.Hash); err != nil {
				return err
			}
			st, err := h.S.Provider.Status(ctx, h.Ref)
			if err != nil {
				return fmt.Errorf("status: %w", err)
			}
			ports := sp.Runtime.GetPorts()
			if len(ports) == 0 {
				return errors.New("the fixture has no container port to route to")
			}
			for _, p := range ports {
				ep := st.Endpoints[p.GetContainerPort()]
				if ep == "" {
					return fmt.Errorf("no endpoint for port %d: %v", p.GetContainerPort(), st.Endpoints)
				}
				if err := h.S.Inspector.Reach(ctx, ep); err != nil {
					return fmt.Errorf("endpoint %s for port %d: %w", ep, p.GetContainerPort(), err)
				}
			}
			// Leaving the ingress drops the endpoints.
			h.ingress = false
			if sp, _, err = h.apply(ctx, h.release); err != nil {
				return err
			}
			if err := h.converged(ctx, sp.Hash); err != nil {
				return err
			}
			if st, err = h.S.Provider.Status(ctx, h.Ref); err != nil {
				return fmt.Errorf("status: %w", err)
			}
			if len(st.Endpoints) != 0 {
				return fmt.Errorf("endpoints without ingress: %v", st.Endpoints)
			}
			return nil
		}},
		{"slots of one app are independent workloads", func(ctx context.Context, h *Harness) error {
			// A zero-downtime deploy runs a second copy of the App beside the first
			// (ADR-0035): applying and destroying it must not touch the first.
			other := h.Ref
			other.Slot = "green"
			sp := h.spec(h.release + 1)
			sp.Ref = other
			if _, err := h.S.Provider.Apply(ctx, provider.ApplyRequest{Spec: sp}); err != nil {
				return fmt.Errorf("apply slot green: %w", err)
			}
			deadline := time.Now().Add(h.S.Timeout)
			for {
				st, err := h.S.Provider.Status(ctx, other)
				if err != nil {
					return fmt.Errorf("status of slot green: %w", err)
				}
				if st.Running && st.Healthy && st.Hash == sp.Hash {
					break
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("slot green not converged: %+v", st)
				}
				time.Sleep(200 * time.Millisecond)
			}
			if err := h.converged(ctx, h.spec(h.release).Hash); err != nil {
				return fmt.Errorf("the first copy after applying slot green: %w", err)
			}
			if err := h.S.Provider.Destroy(ctx, other); err != nil {
				return fmt.Errorf("destroy slot green: %w", err)
			}
			left, err := h.S.Inspector.Leftovers(ctx, other)
			if err != nil {
				return err
			}
			if len(left) != 0 {
				return fmt.Errorf("slot green left %v", left)
			}
			return h.converged(ctx, h.spec(h.release).Hash)
		}},
		{"logs stream and end cleanly", func(ctx context.Context, h *Harness) error {
			out, err := h.logs(ctx)
			if err != nil {
				return err
			}
			if want := h.S.LogMarker(h.release); !strings.Contains(out, want) {
				return fmt.Errorf("logs do not contain %q", want)
			}
			fctx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			seq, err := h.S.Provider.Logs(fctx, provider.LogRequest{Ref: h.Ref, Follow: true})
			if err != nil {
				return fmt.Errorf("follow logs: %w", err)
			}
			done := make(chan error, 1)
			go func() {
				for _, err := range seq {
					if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
						done <- err
						return
					}
				}
				done <- nil
			}()
			select {
			case err := <-done:
				return err
			case <-time.After(10 * time.Second):
				return errors.New("following logs did not end after its context ended")
			}
		}},
		{"exec streams and returns the exit code", func(ctx context.Context, h *Harness) error {
			out, code, err := h.exec(ctx, "echo", "conformance-exec")
			if err != nil {
				return err
			}
			if code != 0 || strings.TrimSpace(out) != "conformance-exec" {
				return fmt.Errorf("echo: exit %d, output %q", code, out)
			}
			if _, code, err = h.exec(ctx, "sh", "-c", "exit 3"); err != nil {
				return err
			}
			if code != 3 {
				return fmt.Errorf("exit code %d, want 3", code)
			}
			return nil
		}},
		{"exec forwards stdin, and a TTY session is sized and resized", func(ctx context.Context, h *Harness) error {
			s, err := h.S.Provider.Exec(ctx, provider.ExecRequest{Ref: h.Ref, Command: []string{"cat"}})
			if err != nil {
				return fmt.Errorf("exec cat: %w", err)
			}
			if _, err := s.Stdin().Write([]byte("conformance-stdin\n")); err != nil {
				_ = s.Close()
				return fmt.Errorf("write stdin: %w", err)
			}
			_ = s.Stdin().Close()
			out, err := io.ReadAll(s.Stdout())
			code, werr := s.Wait()
			_ = s.Close()
			if err != nil || werr != nil || code != 0 || strings.TrimSpace(string(out)) != "conformance-stdin" {
				return fmt.Errorf("cat: exit %d, output %q, %v, %v", code, out, err, werr)
			}

			t, err := h.S.Provider.Exec(ctx, provider.ExecRequest{Ref: h.Ref, Command: []string{"sh"}, TTY: true, Rows: 24, Cols: 80})
			if err != nil {
				return fmt.Errorf("exec with a TTY: %w", err)
			}
			defer func() { _ = t.Close() }()
			if t.Stderr() != nil {
				return errors.New("a TTY session has a separate stderr")
			}
			term := newTermReader(t.Stdout())
			if _, err := t.Stdin().Write([]byte("stty size\n")); err != nil {
				return fmt.Errorf("write to the terminal: %w", err)
			}
			if err := term.waitFor("24 80", 20*time.Second); err != nil {
				return fmt.Errorf("initial size: %w", err)
			}
			if err := t.Resize(40, 100); err != nil {
				return fmt.Errorf("resize: %w", err)
			}
			if _, err := t.Stdin().Write([]byte("stty size; exit 3\n")); err != nil {
				return fmt.Errorf("write to the terminal: %w", err)
			}
			if err := term.waitFor("40 100", 20*time.Second); err != nil {
				return fmt.Errorf("size after resize: %w", err)
			}
			term.drain(20 * time.Second)
			if code, err := t.Wait(); err != nil || code != 3 {
				return fmt.Errorf("TTY session exit %d, %v; want 3", code, err)
			}
			return nil
		}},
		{"destroy leaves nothing and is repeatable", func(ctx context.Context, h *Harness) error {
			if err := h.S.Provider.Destroy(ctx, h.Ref); err != nil {
				return fmt.Errorf("destroy: %w", err)
			}
			st, err := h.S.Provider.Status(ctx, h.Ref)
			if err != nil {
				return fmt.Errorf("status after destroy: %w", err)
			}
			if st.Exists {
				return errors.New("status still reports the workload")
			}
			l, err := h.leftovers(ctx)
			if err != nil {
				return err
			}
			if len(l) != 0 {
				return fmt.Errorf("destroy orphaned %v", l)
			}
			if err := h.S.Provider.Destroy(ctx, h.Ref); err != nil {
				return fmt.Errorf("second destroy: %w", err)
			}
			return nil
		}},
	}
}

// Cleanup destroys the workload if a failed run left it behind.
func (h *Harness) Cleanup(ctx context.Context) {
	_ = h.S.Provider.Destroy(ctx, h.Ref)
}

// termReader collects a terminal's output as it arrives, for waiting on text.
type termReader struct {
	mu   sync.Mutex
	buf  strings.Builder
	done chan struct{}
}

func newTermReader(r io.Reader) *termReader {
	t := &termReader{done: make(chan struct{})}
	go func() {
		defer close(t.done)
		b := make([]byte, 4096)
		for {
			n, err := r.Read(b)
			t.mu.Lock()
			t.buf.Write(b[:n])
			t.mu.Unlock()
			if err != nil {
				// A terminal reports the end of its last writer as an error (EIO).
				return
			}
		}
	}()
	return t
}

func (t *termReader) waitFor(text string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		t.mu.Lock()
		got := t.buf.String()
		t.mu.Unlock()
		if strings.Contains(got, text) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return fmt.Errorf("no %q in terminal output %q", text, t.buf.String())
}

// drain waits until the terminal's output ends.
func (t *termReader) drain(timeout time.Duration) {
	select {
	case <-t.done:
	case <-time.After(timeout):
	}
}

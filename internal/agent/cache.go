package agent

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/nacl/box"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	"github.com/bohumix/bohux/internal/pki"
)

// assignmentFile caches the last accepted signed assignment, secrets still sealed. It
// is what the agent reconciles against while the control plane is away (rule 1).
const assignmentFile = "assignment.pb"

// Errors for an assignment the agent refuses to cache.
var (
	ErrBadSignature = errors.New("assignment signature does not verify")
	ErrWrongNode    = errors.New("assignment is for another node")
)

// Cache holds the node's current assignment: the newest one that verified.
type Cache struct {
	st *State

	mu      sync.Mutex
	current *agentv1.AssignmentPayload
	changed chan struct{}
}

// OpenCache loads the cached assignment, re-verifying it, or starts empty. A cache
// that no longer verifies is moved aside (assignment.pb.corrupt) and the agent starts
// empty and resyncs from the control plane (ADR-0003). An empty cache means "change
// nothing", never "remove every workload".
func OpenCache(st *State) (*Cache, error) {
	c := &Cache{st: st, changed: make(chan struct{}, 1)}
	path := filepath.Join(st.dir, assignmentFile)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read cached assignment: %w", err)
	}
	a := &agentv1.Assignment{}
	p, err := c.decode(raw, a)
	if err != nil {
		slog.Error("cached assignment is corrupt; moved aside, resyncing from the control plane",
			"err", err, "path", path+".corrupt")
		if rerr := os.Rename(path, path+".corrupt"); rerr != nil {
			return nil, fmt.Errorf("move corrupt cache aside: %w", rerr)
		}
		return c, nil
	}
	c.current = p
	return c, nil
}

func (c *Cache) decode(raw []byte, a *agentv1.Assignment) (*agentv1.AssignmentPayload, error) {
	if err := proto.Unmarshal(raw, a); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return c.verify(a)
}

// Generation is the cached generation, 0 if none.
func (c *Cache) Generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current.GetGeneration()
}

// Current returns a copy of the cached assignment, or nil.
func (c *Cache) Current() *agentv1.AssignmentPayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil {
		return nil
	}
	return proto.Clone(c.current).(*agentv1.AssignmentPayload)
}

// Changed is signaled after a new assignment is cached.
func (c *Cache) Changed() <-chan struct{} { return c.changed }

// Accept verifies a and caches it if it is newer than the current one. It returns
// whether it was cached; an older or equal generation is not an error, just ignored.
func (c *Cache) Accept(a *agentv1.Assignment) (bool, error) {
	p, err := c.verify(a)
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if p.GetGeneration() <= c.current.GetGeneration() {
		return false, nil
	}
	raw, err := proto.Marshal(a)
	if err != nil {
		return false, fmt.Errorf("encode assignment: %w", err)
	}
	if err := writeFile(c.st.dir, assignmentFile, raw); err != nil {
		return false, err
	}
	c.current = p
	select {
	case c.changed <- struct{}{}:
	default:
	}
	return true, nil
}

func (c *Cache) verify(a *agentv1.Assignment) (*agentv1.AssignmentPayload, error) {
	pub := c.st.SigningPublicKey()
	if a.GetKeyId() != pki.KeyID(pub) ||
		!pki.VerifyAssignment(ed25519.PublicKey(pub), a.GetPayload(), a.GetSignature()) {
		return nil, ErrBadSignature
	}
	p := &agentv1.AssignmentPayload{}
	if err := proto.Unmarshal(a.GetPayload(), p); err != nil {
		return nil, fmt.Errorf("decode assignment payload: %w", err)
	}
	if p.GetNodeId() != c.st.NodeID() {
		return nil, ErrWrongNode
	}
	return p, nil
}

// OpenSecrets unseals a workload's secret values with the node's sealing key
// (ADR-0008). Only the provider calls it, to render its secret file; the values must
// never be logged or written anywhere else (rule 3).
func (s *State) OpenSecrets(w *agentv1.Workload) (map[string]string, error) {
	vals, err := s.openSealed(w)
	if err != nil {
		return nil, err
	}
	return vals.GetValues(), nil
}

// OpenPullConfig returns the workload's sealed registry credential, empty if it has
// none (ADR-0041).
func (s *State) OpenPullConfig(w *agentv1.Workload) (string, error) {
	vals, err := s.openSealed(w)
	if err != nil {
		return "", err
	}
	return vals.GetImagePullConfig(), nil
}

func (s *State) openSealed(w *agentv1.Workload) (*agentv1.SecretValues, error) {
	if len(w.GetSealedSecrets()) == 0 {
		return &agentv1.SecretValues{Values: map[string]string{}}, nil
	}
	var pub, priv [32]byte
	copy(pub[:], s.sealingKey.PublicKey().Bytes())
	copy(priv[:], s.sealingKey.Bytes())
	plain, ok := box.OpenAnonymous(nil, w.GetSealedSecrets(), &pub, &priv)
	if !ok {
		return nil, errors.New("sealed secrets do not open with this node's key")
	}
	vals := &agentv1.SecretValues{}
	if err := proto.Unmarshal(plain, vals); err != nil {
		return nil, fmt.Errorf("decode secret values: %w", err)
	}
	if vals.Values == nil {
		vals.Values = map[string]string{}
	}
	return vals, nil
}

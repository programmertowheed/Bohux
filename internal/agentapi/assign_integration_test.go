//go:build integration

package agentapi

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/nacl/box"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/bohumix/bohux/gen/bohux/agent/v1"
	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/store/fixture"
)

type fakeSecrets map[string]string

func (f fakeSecrets) Resolve(_ context.Context, _ uuid.UUID, handles []string) (map[string]string, error) {
	out := map[string]string{}
	for _, h := range handles {
		if v, ok := f[h]; ok {
			out[h] = v
		}
	}
	return out, nil
}

const secretValue = "postgres://user:hunter2@db/app"

func workload(app string) *agentv1.Workload {
	return &agentv1.Workload{
		AppId: app, Provider: "docker", Image: "ghcr.io/acme/api@sha256:" + string(bytes.Repeat([]byte("a"), 64)),
		Runtime: &bohuxv1.Runtime{Replicas: 1},
		Secrets: []*bohuxv1.SecretRef{{Key: "DATABASE_URL", Handle: "openbao:db"}},
	}
}

var system = store.Actor{ID: store.SystemActorID, Kind: store.ActorSystem}

// nextAssignment reads messages until an Assignment arrives.
func nextAssignment(t *testing.T, s interface {
	Receive() (*agentv1.ConnectResponse, error)
}) *agentv1.Assignment {
	t.Helper()
	for i := 0; i < 200; i++ {
		msg, err := s.Receive()
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if a := msg.GetAssignment(); a != nil {
			return a
		}
	}
	t.Fatal("no assignment arrived")
	return nil
}

func TestAssignments(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	a := h.register("vps-1")
	node := uuid.MustParse(a.node)
	app1, env1 := fixture.App(t, h.st, h.tenant, "api", "prod")
	app2, _ := fixture.App(t, h.st, h.tenant, "worker", "staging")

	gen, err := h.srv.Assign(ctx, h.tenant, node, []*agentv1.Workload{workload(app1.String())}, system)
	if err != nil || gen != 1 {
		t.Fatalf("Assign = %d, %v", gen, err)
	}

	sctx, cancel := context.WithCancel(ctx)
	stream, first, err := open(sctx, a.client, hello(a.node, "0.3.0"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStream(stream, cancel)
	got := first.GetAssignment()
	if got == nil {
		got = nextAssignment(t, stream)
	}

	t.Run("signed, for this node, with secrets sealed to it", func(t *testing.T) {
		if !pki.VerifyAssignment(h.pki.SigningPublicKey(), got.GetPayload(), got.GetSignature()) {
			t.Fatal("signature does not verify")
		}
		if got.GetKeyId() != pki.KeyID(h.pki.SigningPublicKey()) {
			t.Errorf("key id %q", got.GetKeyId())
		}
		p := &agentv1.AssignmentPayload{}
		if err := proto.Unmarshal(got.GetPayload(), p); err != nil {
			t.Fatal(err)
		}
		if p.GetNodeId() != a.node || p.GetGeneration() != 1 || len(p.GetWorkloads()) != 1 {
			t.Fatalf("payload %v", p)
		}
		sealed := p.GetWorkloads()[0].GetSealedSecrets()
		if bytes.Contains(got.GetPayload(), []byte("hunter2")) {
			t.Error("secret value in clear in the payload")
		}
		var pub, priv [32]byte
		copy(pub[:], a.sealing.PublicKey().Bytes())
		copy(priv[:], a.sealing.Bytes())
		plain, ok := box.OpenAnonymous(nil, sealed, &pub, &priv)
		if !ok {
			t.Fatal("sealed secrets do not open with the node's key")
		}
		vals := &agentv1.SecretValues{}
		if err := proto.Unmarshal(plain, vals); err != nil {
			t.Fatal(err)
		}
		if vals.GetValues()["DATABASE_URL"] != secretValue {
			t.Errorf("opened values %v", vals.GetValues())
		}
	})

	t.Run("the control plane names each workload from its App", func(t *testing.T) {
		p := &agentv1.AssignmentPayload{}
		if err := proto.Unmarshal(got.GetPayload(), p); err != nil {
			t.Fatal(err)
		}
		w := p.GetWorkloads()[0]
		if w.GetAppSlug() != "api" || w.GetEnvironmentSlug() != "prod" || w.GetEnvironmentId() != env1.String() {
			t.Errorf("workload named %q/%q in %q, want api/prod in %s",
				w.GetAppSlug(), w.GetEnvironmentSlug(), w.GetEnvironmentId(), env1)
		}
	})

	t.Run("nothing secret is stored", func(t *testing.T) {
		var payload []byte
		err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
			return tx.QueryRow(ctx, `SELECT payload FROM assignments WHERE node_id = $1 AND generation = 1`,
				node).Scan(&payload)
		})
		if err != nil {
			t.Fatal(err)
		}
		p := &agentv1.AssignmentPayload{}
		if err := proto.Unmarshal(payload, p); err != nil {
			t.Fatal(err)
		}
		if len(p.GetWorkloads()[0].GetSealedSecrets()) != 0 || bytes.Contains(payload, []byte("hunter2")) {
			t.Error("stored assignment holds secret material")
		}
	})

	t.Run("a new generation is pushed to the open stream", func(t *testing.T) {
		gen, err := h.srv.Assign(ctx, h.tenant, node, []*agentv1.Workload{workload(app1.String()), workload(app2.String())}, system)
		if err != nil || gen != 2 {
			t.Fatalf("Assign = %d, %v", gen, err)
		}
		next := nextAssignment(t, stream)
		p := &agentv1.AssignmentPayload{}
		if err := proto.Unmarshal(next.GetPayload(), p); err != nil {
			t.Fatal(err)
		}
		if p.GetGeneration() != 2 || len(p.GetWorkloads()) != 2 {
			t.Errorf("pushed %v", p)
		}
	})

	t.Run("acks are recorded, bogus ones ignored", func(t *testing.T) {
		for _, g := range []uint64{2, 99} {
			if err := stream.Send(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_Ack{
				Ack: &agentv1.Ack{Generation: g}}}); err != nil {
				t.Fatal(err)
			}
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			var acked *int64
			err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
				return tx.QueryRow(ctx, `SELECT acked_generation FROM nodes_heads WHERE id = $1`, node).Scan(&acked)
			})
			if err != nil {
				t.Fatal(err)
			}
			if acked != nil && *acked == 2 {
				break
			}
			if acked != nil && *acked != 2 || time.Now().After(deadline) {
				t.Fatalf("acked_generation = %v, want 2", acked)
			}
			time.Sleep(20 * time.Millisecond)
		}
	})

	t.Run("an up-to-date agent is not sent the assignment again", func(t *testing.T) {
		closeStream(stream, cancel)
		b := h.register("vps-2")
		if _, err := h.srv.Assign(ctx, h.tenant, uuid.MustParse(b.node), []*agentv1.Workload{workload(app1.String())}, system); err != nil {
			t.Fatal(err)
		}
		req := hello(b.node, "0.3.0")
		req.GetHello().AppliedGeneration = 1
		s2ctx, cancel2 := context.WithCancel(ctx)
		s2, first, err := open(s2ctx, b.client, req)
		if err != nil {
			t.Fatal(err)
		}
		defer closeStream(s2, cancel2)
		if first.GetAssignment() != nil {
			t.Error("sent an assignment the agent already has")
		}
	})

	t.Run("assigning to an unknown or revoked node fails", func(t *testing.T) {
		_, err := h.srv.Assign(ctx, h.tenant, uuid.Must(uuid.NewV7()), nil, system)
		if err == nil {
			t.Error("assigned to an unknown node")
		}
		_, err = h.srv.Assign(ctx, h.tenant, node, []*agentv1.Workload{{AppId: app1.String(), SealedSecrets: []byte{1}}}, system)
		if err == nil {
			t.Error("accepted pre-sealed secrets")
		}
	})

	t.Run("assigning an unknown app or the wrong environment fails", func(t *testing.T) {
		tests := []struct {
			name string
			w    *agentv1.Workload
			want error
		}{
			{"not an ID", workload("app-1"), ErrUnknownApp},
			{"no such app", workload(uuid.Must(uuid.NewV7()).String()), ErrUnknownApp},
			{"other environment", func() *agentv1.Workload {
				w := workload(app1.String())
				w.EnvironmentId = uuid.Must(uuid.NewV7()).String()
				return w
			}(), ErrWrongEnv},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := h.srv.Assign(ctx, h.tenant, node, []*agentv1.Workload{tt.w}, system)
				if !errors.Is(err, tt.want) {
					t.Errorf("Assign = %v, want %v", err, tt.want)
				}
			})
		}
	})

}

func TestAssignApp(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	node := uuid.MustParse(h.register("vps-1").node)
	app1, _ := fixture.App(t, h.st, h.tenant, "api", "prod")
	app2, _ := fixture.App(t, h.st, h.tenant, "worker", "prod2")

	deployed := func(app uuid.UUID, release, deployment string) *agentv1.Workload {
		w := workload(app.String())
		w.Secrets = nil
		w.ReleaseId, w.DeploymentId = release, deployment
		return w
	}
	latest := func() []*agentv1.Workload {
		t.Helper()
		var raw []byte
		err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
			return tx.QueryRow(ctx, `SELECT payload FROM assignments WHERE node_id = $1
				ORDER BY generation DESC LIMIT 1`, node).Scan(&raw)
		})
		if err != nil {
			t.Fatalf("read latest: %v", err)
		}
		p := &agentv1.AssignmentPayload{}
		if err := proto.Unmarshal(raw, p); err != nil {
			t.Fatal(err)
		}
		return p.GetWorkloads()
	}
	releaseOf := func(ws []*agentv1.Workload) map[string]string {
		out := map[string]string{}
		for _, w := range ws {
			out[w.GetAppId()] = w.GetReleaseId()
		}
		return out
	}
	assign := func(app uuid.UUID, w *agentv1.Workload) uint64 {
		t.Helper()
		gen, err := h.srv.AssignApp(ctx, h.tenant, node, app, w, time.Time{}, system)
		if err != nil {
			t.Fatalf("AssignApp: %v", err)
		}
		return gen
	}

	if gen := assign(app1, nil); gen != 0 {
		t.Errorf("removing from an empty node wrote generation %d", gen)
	}
	if gen := assign(app1, deployed(app1, "r1", "d1")); gen != 1 {
		t.Fatalf("first assign = generation %d, want 1", gen)
	}
	if gen := assign(app1, deployed(app1, "r1", "d1")); gen != 1 {
		t.Errorf("repeat of the same deployment wrote generation %d, want 1", gen)
	}
	if gen := assign(app2, deployed(app2, "w1", "d2")); gen != 2 {
		t.Errorf("second app = generation %d, want 2", gen)
	}
	if gen := assign(app1, deployed(app1, "r2", "d3")); gen != 3 {
		t.Errorf("new release = generation %d, want 3", gen)
	}
	got := releaseOf(latest())
	if len(got) != 2 || got[app1.String()] != "r2" || got[app2.String()] != "w1" {
		t.Errorf("latest = %v, want api at r2 and worker at w1", got)
	}
	for _, w := range latest() {
		if w.GetAppSlug() == "" || w.GetEnvironmentSlug() == "" {
			t.Errorf("workload %s was not named", w.GetAppId())
		}
	}
	if gen := assign(app1, nil); gen != 4 {
		t.Errorf("removal = generation %d, want 4", gen)
	}
	if got := releaseOf(latest()); len(got) != 1 || got[app2.String()] != "w1" {
		t.Errorf("after removal = %v, want only the worker", got)
	}
	if gen := assign(app1, nil); gen != 4 {
		t.Errorf("repeat removal wrote generation %d, want 4", gen)
	}

	t.Run("a forward deploy carries the workload it replaces", func(t *testing.T) {
		after := time.Now().Add(7 * time.Minute).UTC().Truncate(time.Second)
		if _, err := h.srv.AssignApp(ctx, h.tenant, node, app2, deployed(app2, "w2", "d5"), after, system); err != nil {
			t.Fatal(err)
		}
		var w *agentv1.Workload
		for _, x := range latest() {
			if x.GetAppId() == app2.String() {
				w = x
			}
		}
		fb := w.GetFallback()
		if fb.GetReleaseId() != "w1" || fb.GetFallback() != nil || !w.GetFallbackAfter().AsTime().Equal(after) {
			t.Errorf("workload = %v, want fallback w1 (not nested) after %v", w, after)
		}
		// Compensation back to w1 carries none.
		if _, err := h.srv.AssignApp(ctx, h.tenant, node, app2, deployed(app2, "w1", "d6"), time.Time{}, system); err != nil {
			t.Fatal(err)
		}
		for _, x := range latest() {
			if x.GetAppId() == app2.String() && x.GetFallback() != nil {
				t.Errorf("compensation carries a fallback: %v", x)
			}
		}
	})

	t.Run("promoting a canary is a change; repeating it is not", func(t *testing.T) {
		canary := deployed(app1, "c1", "d7")
		canary.CanaryWeight = 10
		gen := assign(app1, canary)
		if again := assign(app1, canary); again != gen {
			t.Errorf("repeat of the canary wrote generation %d, want %d", again, gen)
		}
		full := deployed(app1, "c1", "d7")
		promoted := assign(app1, full)
		if promoted != gen+1 {
			t.Errorf("promotion (weight 10 to 0) = generation %d, want %d", promoted, gen+1)
		}
		if again := assign(app1, full); again != promoted {
			t.Errorf("repeat of the promotion wrote generation %d, want %d", again, promoted)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		if _, err := h.srv.AssignApp(ctx, h.tenant, node, app1, deployed(app2, "x", "y"), time.Time{}, system); !errors.Is(err, ErrUnknownApp) {
			t.Errorf("mismatched app = %v, want ErrUnknownApp", err)
		}
		ghost := uuid.Must(uuid.NewV7())
		if _, err := h.srv.AssignApp(ctx, h.tenant, node, ghost, deployed(ghost, "x", "y"), time.Time{}, system); !errors.Is(err, ErrUnknownApp) {
			t.Errorf("unknown app = %v, want ErrUnknownApp", err)
		}
		if _, err := h.srv.AssignApp(ctx, h.tenant, uuid.Must(uuid.NewV7()), app1, nil, time.Time{}, system); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("unknown node = %v, want ErrNotFound", err)
		}
	})
}

func TestFollowAppTx(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	node := uuid.MustParse(h.register("vps-1").node)
	app1, _ := fixture.App(t, h.st, h.tenant, "api", "prod")
	app2, _ := fixture.App(t, h.st, h.tenant, "worker", "prod2")
	var routes []*bohuxv1.Route
	scale := func(app uuid.UUID, replicas int32) bool {
		t.Helper()
		var written bool
		if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
			var err error
			written, err = h.srv.FollowAppTx(ctx, tx, node, app, replicas, routes, system)
			return err
		}); err != nil {
			t.Fatalf("FollowAppTx: %v", err)
		}
		return written
	}
	if scale(app1, 3) {
		t.Error("scaling an app with no workload wrote a generation")
	}
	w1, w2 := workload(app1.String()), workload(app2.String())
	w1.Secrets, w2.Secrets = nil, nil
	if _, err := h.srv.Assign(ctx, h.tenant, node, []*agentv1.Workload{w1, w2}, system); err != nil {
		t.Fatal(err)
	}
	if !scale(app1, 3) {
		t.Fatal("scale wrote nothing")
	}
	if scale(app1, 3) {
		t.Error("scaling to the current replicas wrote again")
	}
	var raw []byte
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		return tx.QueryRow(ctx, `SELECT payload FROM assignments WHERE node_id = $1
			ORDER BY generation DESC LIMIT 1`, node).Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	p := &agentv1.AssignmentPayload{}
	if err := proto.Unmarshal(raw, p); err != nil {
		t.Fatal(err)
	}
	if p.GetGeneration() != 2 {
		t.Errorf("generation = %d, want 2", p.GetGeneration())
	}
	for _, w := range p.GetWorkloads() {
		want := int32(1)
		if w.GetAppId() == app1.String() {
			want = 3
		}
		if got := w.GetRuntime().GetReplicas(); got != want {
			t.Errorf("app %s replicas = %d, want %d", w.GetAppSlug(), got, want)
		}
	}

	// Routes follow the same way (ADR-0034), and only for this App.
	routes = []*bohuxv1.Route{{Host: "api.example.com", Port: 8080}}
	if !scale(app1, 3) {
		t.Fatal("a route change wrote nothing")
	}
	if scale(app1, 3) {
		t.Error("the same routes wrote again")
	}
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		return tx.QueryRow(ctx, `SELECT payload FROM assignments WHERE node_id = $1
			ORDER BY generation DESC LIMIT 1`, node).Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	p = &agentv1.AssignmentPayload{}
	if err := proto.Unmarshal(raw, p); err != nil {
		t.Fatal(err)
	}
	for _, w := range p.GetWorkloads() {
		want := 0
		if w.GetAppId() == app1.String() {
			want = 1
		}
		if len(w.GetRoutes()) != want {
			t.Errorf("app %s routes = %v, want %d", w.GetAppSlug(), w.GetRoutes(), want)
		}
	}
}

// TestRotateTx checks a rotated secret reaches the node that uses it as a new,
// sealed generation, and leaves other nodes alone (ADR-0008).
func TestRotateTx(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	secrets := fakeSecrets{"openbao:db": "postgres://user:first-hunter2@db/app"}
	h.srv.Secrets = secrets
	a1, a2 := h.register("vps-1"), h.register("vps-2")
	node1, node2 := uuid.MustParse(a1.node), uuid.MustParse(a2.node)
	app1, _ := fixture.App(t, h.st, h.tenant, "api", "prod")
	app2, _ := fixture.App(t, h.st, h.tenant, "worker", "prod2")
	if _, err := h.srv.Assign(ctx, h.tenant, node1, []*agentv1.Workload{workload(app1.String())}, system); err != nil {
		t.Fatal(err)
	}
	plain := workload(app2.String())
	plain.Secrets = nil
	if _, err := h.srv.Assign(ctx, h.tenant, node2, []*agentv1.Workload{plain}, system); err != nil {
		t.Fatal(err)
	}

	sctx, cancel := context.WithCancel(ctx)
	stream, first, err := open(sctx, a1.client, hello(a1.node, "0.3.0"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStream(stream, cancel)
	if first.GetAssignment() == nil {
		nextAssignment(t, stream)
	}

	secrets["openbao:db"] = "postgres://user:second-hunter2@db/app"
	var woken []uuid.UUID
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		var err error
		woken, err = h.srv.RotateTx(ctx, tx, "openbao:db", system)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(woken) != 1 || woken[0] != node1 {
		t.Fatalf("woken = %v, want only %s", woken, node1)
	}
	for _, n := range woken {
		h.srv.Wake(n)
	}
	got := nextAssignment(t, stream)
	p := &agentv1.AssignmentPayload{}
	if err := proto.Unmarshal(got.GetPayload(), p); err != nil {
		t.Fatal(err)
	}
	if p.GetGeneration() != 2 {
		t.Fatalf("generation = %d, want 2", p.GetGeneration())
	}
	var pub, priv [32]byte
	copy(pub[:], a1.sealing.PublicKey().Bytes())
	copy(priv[:], a1.sealing.Bytes())
	opened, ok := box.OpenAnonymous(nil, p.GetWorkloads()[0].GetSealedSecrets(), &pub, &priv)
	if !ok {
		t.Fatal("rotated values do not open with the node's key")
	}
	vals := &agentv1.SecretValues{}
	if err := proto.Unmarshal(opened, vals); err != nil {
		t.Fatal(err)
	}
	if vals.GetValues()["DATABASE_URL"] != "postgres://user:second-hunter2@db/app" {
		t.Errorf("node got %v, want the rotated value", vals.GetValues())
	}

	var gen2 int64
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		return tx.QueryRow(ctx, `SELECT max(generation) FROM assignments WHERE node_id = $1`, node2).Scan(&gen2)
	}); err != nil || gen2 != 1 {
		t.Errorf("node without the secret is at generation %d, %v; want 1", gen2, err)
	}
}

// A registry credential travels sealed in its own field, never among the App's
// environment variables, and rotating it re-sends the assignment (ADR-0041).
func TestImagePullSecret(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	const first, second = `{"auths":{"r.example":{"auth":"Zmlyc3Q="}}}`, `{"auths":{"r.example":{"auth":"c2Vjb25k"}}}`
	secrets := fakeSecrets{"openbao:db": secretValue, "openbao:reg": first}
	h.srv.Secrets = secrets
	a := h.register("vps-1")
	node := uuid.MustParse(a.node)
	app, _ := fixture.App(t, h.st, h.tenant, "api", "prod")
	w := workload(app.String())
	w.ImagePullSecret = &bohuxv1.SecretRef{Key: "REGISTRY", Handle: "openbao:reg"}
	if _, err := h.srv.Assign(ctx, h.tenant, node, []*agentv1.Workload{w}, system); err != nil {
		t.Fatal(err)
	}

	sctx, cancel := context.WithCancel(ctx)
	stream, firstMsg, err := open(sctx, a.client, hello(a.node, "0.3.0"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeStream(stream, cancel)
	got := firstMsg.GetAssignment()
	if got == nil {
		got = nextAssignment(t, stream)
	}
	opened := func(asg *agentv1.Assignment) *agentv1.SecretValues {
		t.Helper()
		p := &agentv1.AssignmentPayload{}
		if err := proto.Unmarshal(asg.GetPayload(), p); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(asg.GetPayload(), []byte("Zmlyc3Q")) || bytes.Contains(asg.GetPayload(), []byte("c2Vjb25k")) {
			t.Error("registry credential in clear in the payload")
		}
		var pub, priv [32]byte
		copy(pub[:], a.sealing.PublicKey().Bytes())
		copy(priv[:], a.sealing.Bytes())
		plain, ok := box.OpenAnonymous(nil, p.GetWorkloads()[0].GetSealedSecrets(), &pub, &priv)
		if !ok {
			t.Fatal("sealed secrets do not open with the node's key")
		}
		vals := &agentv1.SecretValues{}
		if err := proto.Unmarshal(plain, vals); err != nil {
			t.Fatal(err)
		}
		return vals
	}
	vals := opened(got)
	if vals.GetImagePullConfig() != first {
		t.Errorf("image_pull_config = %q, want the credential", vals.GetImagePullConfig())
	}
	if _, leaked := vals.GetValues()["REGISTRY"]; leaked || len(vals.GetValues()) != 1 {
		t.Errorf("environment values = %v, want only DATABASE_URL", vals.GetValues())
	}

	secrets["openbao:reg"] = second
	var woken []uuid.UUID
	if err := h.st.InTenant(ctx, h.tenant, func(tx *store.Tx) error {
		var err error
		woken, err = h.srv.RotateTx(ctx, tx, "openbao:reg", system)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(woken) != 1 || woken[0] != node {
		t.Fatalf("woken = %v, want %s", woken, node)
	}
	h.srv.Wake(node)
	if vals := opened(nextAssignment(t, stream)); vals.GetImagePullConfig() != second {
		t.Errorf("after rotation image_pull_config = %q, want the new credential", vals.GetImagePullConfig())
	}
}

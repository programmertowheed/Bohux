package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
)

// deploy creates a Release (from a Git ref, an image, or the App's own source) and
// deploys it, following the Deployment until it ends.
func (c *command) deploy(ctx context.Context, args []string) error {
	fs, t := c.flags("deploy")
	ref := fs.String("ref", "", "git branch, tag, or commit to build")
	image := fs.String("image", "", "prebuilt image to deploy, as repository@sha256:<digest>")
	reason := fs.String("reason", "", "why, recorded with the deployment")
	slug, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *ref != "" && *image != "" {
		return fmt.Errorf("deploy: --ref and --image are exclusive: %w", ErrUsage)
	}
	req := &bohuxv1.CreateReleaseRequest{IdempotencyKey: key()}
	switch {
	case *ref != "":
		req.From = &bohuxv1.CreateReleaseRequest_GitRef{GitRef: *ref}
	case *image != "":
		img, err := parseImage(*image)
		if err != nil {
			return err
		}
		req.From = &bohuxv1.CreateReleaseRequest_Image{Image: img}
	}
	if err := c.connect(); err != nil {
		return err
	}
	app, err := c.app(ctx, t, slug)
	if err != nil {
		return err
	}
	req.AppId = app.GetMeta().GetId()
	warnDowntime(c, app)
	rel, err := c.releases.CreateRelease(ctx, call(c.token, req))
	if err != nil {
		return fmt.Errorf("create release: %w", err)
	}
	r := rel.Msg.GetRelease()
	c.printf("release %s %s (%s)\n", r.GetMeta().GetId(), state(r.GetState().String()), r.GetSourceRef())
	dep, err := c.deployments.CreateDeployment(ctx, call(c.token, &bohuxv1.CreateDeploymentRequest{
		ReleaseId: r.GetMeta().GetId(), EnvironmentId: app.GetEnvironmentId(), Reason: *reason, IdempotencyKey: key()}))
	if err != nil {
		return fmt.Errorf("create deployment: %w", err)
	}
	return c.follow(ctx, dep.Msg.GetDeployment())
}

// rollback deploys the App's last healthy Release, or the one named.
func (c *command) rollback(ctx context.Context, args []string) error {
	fs, t := c.flags("rollback")
	to := fs.String("to", "", "release ID to roll back to (default: the last healthy one before the current)")
	reason := fs.String("reason", "", "why, recorded with the deployment")
	slug, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := c.connect(); err != nil {
		return err
	}
	app, err := c.app(ctx, t, slug)
	if err != nil {
		return err
	}
	warnDowntime(c, app)
	r, err := c.deployments.Rollback(ctx, call(c.token, &bohuxv1.RollbackRequest{
		AppId: app.GetMeta().GetId(), ToReleaseId: *to, Reason: *reason, IdempotencyKey: key()}))
	if err != nil {
		return fmt.Errorf("rollback: %w", err)
	}
	c.printf("rolling back to release %s\n", r.Msg.GetDeployment().GetReleaseId())
	return c.follow(ctx, r.Msg.GetDeployment())
}

// scale sets the App's replicas; the node follows at once (ADR-0027).
func (c *command) scale(ctx context.Context, args []string) error {
	fs, t := c.flags("scale")
	replicas := fs.Int("replicas", -1, "number of replicas (required)")
	slug, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *replicas < 0 {
		return fmt.Errorf("scale: --replicas is required: %w", ErrUsage)
	}
	if err := c.connect(); err != nil {
		return err
	}
	app, err := c.app(ctx, t, slug)
	if err != nil {
		return err
	}
	r, err := c.apps.ScaleApp(ctx, call(c.token, &bohuxv1.ScaleAppRequest{Id: app.GetMeta().GetId(),
		Replicas: int32(*replicas), ExpectedRevisionId: app.GetMeta().GetRevisionId(), IdempotencyKey: key()}))
	if err != nil {
		return fmt.Errorf("scale: %w", err)
	}
	c.printf("%s scaled to %d replicas\n", slug, r.Msg.GetApp().GetRuntime().GetReplicas())
	return nil
}

// follow prints each state the Deployment passes through until it ends, and fails
// unless it succeeded.
func (c *command) follow(ctx context.Context, d *bohuxv1.Deployment) error {
	start := time.Now()
	c.printf("deployment %s\n", d.GetMeta().GetId())
	stream, err := c.deployments.WatchDeployment(ctx, call(c.token, &bohuxv1.WatchDeploymentRequest{Id: d.GetMeta().GetId()}))
	if err != nil {
		return fmt.Errorf("watch deployment: %w", err)
	}
	defer func() { _ = stream.Close() }()
	last := ""
	for stream.Receive() {
		d = stream.Msg().GetDeployment()
		if s := state(d.GetState().String()); s != last {
			last = s
			c.printf("  %-12s %s\n", s, time.Since(start).Round(100*time.Millisecond))
		}
	}
	if err := stream.Err(); err != nil {
		return fmt.Errorf("watch deployment: %w", err)
	}
	if d.GetState() != bohuxv1.DeploymentState_DEPLOYMENT_STATE_SUCCEEDED {
		msg := "deployment ended " + state(d.GetState().String())
		if f := d.GetFailure().GetReason(); f != "" {
			msg += ": " + f
		}
		return errors.New(msg)
	}
	return nil
}

// warnDowntime says so up front when a deploy stops the old container before the new
// one can bind its host port (ADR-0020 item 5).
func warnDowntime(c *command, app *bohuxv1.App) {
	for _, p := range app.GetRuntime().GetPorts() {
		if p.GetHostPort() != 0 {
			c.printf("note: %s publishes host port %d; the old container stops before the new one starts, "+
				"so the port is briefly unavailable (measured 0.4-1.5 s). Zero-downtime deploys need ingress (v0.2).\n",
				app.GetSlug(), p.GetHostPort())
			return
		}
	}
}

// parseImage splits repository@sha256:<digest>.
func parseImage(s string) (*bohuxv1.ImageSource, error) {
	repo, digest, ok := strings.Cut(s, "@")
	if !ok || repo == "" || !strings.HasPrefix(digest, "sha256:") {
		return nil, fmt.Errorf("--image must be repository@sha256:<digest>, never a tag: %w", ErrUsage)
	}
	return &bohuxv1.ImageSource{Repository: repo, Digest: digest}, nil
}

// state turns DEPLOYMENT_STATE_HEALTH_GATE into health_gate.
func state(enum string) string {
	for _, p := range []string{"DEPLOYMENT_STATE_", "RELEASE_STATE_"} {
		enum = strings.TrimPrefix(enum, p)
	}
	return strings.ToLower(enum)
}

func (c *command) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(c.out, format, args...)
}

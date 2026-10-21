package api

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/gen/bohux/v1/bohuxv1connect"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/store"
)

const releaseType = "bohux.v1.Release"

// errBuildNotYet answers a Release that needs a build: BuildKit arrives with S7.
var errBuildNotYet = connect.NewError(connect.CodeUnimplemented,
	errors.New("building a release from git is not available yet; create it from an image"))

type releaseService struct {
	s *Server
}

// CreateRelease imports an image as a ready Release that captures the App's current
// revision (ADR-0010). An explicit image wins over the App's image source; a Git
// source needs a build, which is not available yet.
func (rs *releaseService) CreateRelease(ctx context.Context, req *connect.Request[bohuxv1.CreateReleaseRequest]) (*connect.Response[bohuxv1.CreateReleaseResponse], error) {
	msg := req.Msg
	p, _ := auth.FromContext(ctx)
	appID, err := parseID("app_id", msg.GetAppId())
	if err != nil {
		return nil, err
	}
	if msg.GetGitRef() != "" {
		return nil, errBuildNotYet
	}
	if img := msg.GetImage(); img != nil {
		if err := validSource(&bohuxv1.Source{Kind: &bohuxv1.Source_Image{Image: img}}); err != nil {
			return nil, err
		}
	}
	idem, err := newIdempotency(ctx, bohuxv1connect.ReleaseServiceCreateReleaseProcedure, msg.GetIdempotencyKey(), msg)
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	resp := &bohuxv1.CreateReleaseResponse{}
	err = rs.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		prior, err := idem.lookup(ctx, tx)
		if err != nil {
			return err
		}
		if prior != nil {
			resp.Release, err = getRelease(ctx, tx, prior.ResourceID)
			return err
		}
		project, _, err := appProject(ctx, tx, appID)
		if err != nil {
			return err
		}
		if err := authorizeProject(ctx, project, auth.RoleDeployer); err != nil {
			return err
		}
		app, err := getApp(ctx, tx, appID, uuid.Nil, "")
		if err != nil {
			return err
		}
		img := msg.GetImage()
		if img == nil {
			img = app.GetSource().GetImage()
		}
		if img == nil {
			return errBuildNotYet
		}
		config, err := uuid.Parse(app.GetMeta().GetRevisionId())
		if err != nil {
			return fmt.Errorf("stored app revision: %w", err)
		}
		id, rev, err := tx.Create(ctx, "releases",
			[]store.Col{{Name: "app_id", Value: appID}, {Name: "config_revision_id", Value: config}},
			[]store.Col{
				{Name: "state", Value: "ready"}, {Name: "artifact_digest", Value: img.GetDigest()},
				// The pinned reference is what the node pulls and runs.
				{Name: "source_ref", Value: img.GetRepository() + "@" + img.GetDigest()},
				{Name: "artifact_available", Value: true},
			},
			store.Audit{Actor: p.Actor(), Action: bohuxv1connect.ReleaseServiceCreateReleaseProcedure,
				ResourceType: releaseType, ProjectID: project})
		if err != nil {
			return err
		}
		if err := idem.record(ctx, tx, releaseType, id, rev); err != nil {
			return err
		}
		resp.Release, err = getRelease(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

func (rs *releaseService) GetRelease(ctx context.Context, req *connect.Request[bohuxv1.GetReleaseRequest]) (*connect.Response[bohuxv1.GetReleaseResponse], error) {
	p, _ := auth.FromContext(ctx)
	id, err := parseID("id", req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	resp := &bohuxv1.GetReleaseResponse{}
	err = rs.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		rel, err := getRelease(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := authorizeApp(ctx, tx, rel.GetAppId(), auth.RoleViewer); err != nil {
			return err
		}
		resp.Release = rel
		return nil
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	return connect.NewResponse(resp), nil
}

// ListReleases lists an App's Releases, newest first.
func (rs *releaseService) ListReleases(ctx context.Context, req *connect.Request[bohuxv1.ListReleasesRequest]) (*connect.Response[bohuxv1.ListReleasesResponse], error) {
	p, _ := auth.FromContext(ctx)
	appID, err := parseID("app_id", req.Msg.GetAppId())
	if err != nil {
		return nil, err
	}
	size, before, err := page(req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	resp := &bohuxv1.ListReleasesResponse{}
	err = rs.s.Store.InTenant(ctx, p.Tenant, func(tx *store.Tx) error {
		if err := authorizeApp(ctx, tx, appID.String(), auth.RoleViewer); err != nil {
			return err
		}
		// IDs are UUIDv7, so descending ID order is newest first.
		rows, err := tx.Query(ctx, releaseSelect+`
			WHERE NOT h.deleted AND h.app_id = $1 AND ($2::uuid IS NULL OR h.id < $2)
			ORDER BY h.id DESC LIMIT $3`, appID, before, size+1)
		if err != nil {
			return fmt.Errorf("list releases: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			rel, err := scanRelease(rows)
			if err != nil {
				return err
			}
			resp.Releases = append(resp.Releases, rel)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, toConnect(ctx, err)
	}
	if len(resp.Releases) > size {
		resp.Releases = resp.Releases[:size]
		resp.NextPageToken = resp.Releases[size-1].GetMeta().GetId()
	}
	return connect.NewResponse(resp), nil
}

// authorizeApp checks the caller holds min on the live App's project; an App the
// caller cannot see is not found.
func authorizeApp(ctx context.Context, tx *store.Tx, rawApp string, min auth.Role) error {
	app, err := uuid.Parse(rawApp)
	if err != nil {
		return fmt.Errorf("stored app id: %w", err)
	}
	project, _, err := appProject(ctx, tx, app)
	if err != nil {
		return err
	}
	return authorizeProject(ctx, project, min)
}

const releaseSelect = `SELECT ` + metaColumns + `, h.app_id, h.config_revision_id, r.state,
	r.artifact_digest, r.source_ref, r.provenance_ref, r.sbom_ref, r.build_workflow_id,
	r.artifact_available, r.failure
	FROM releases_heads h JOIN releases_revisions r ON r.id = h.revision_id`

var releaseStates = map[string]bohuxv1.ReleaseState{
	"building": bohuxv1.ReleaseState_RELEASE_STATE_BUILDING,
	"ready":    bohuxv1.ReleaseState_RELEASE_STATE_READY,
	"failed":   bohuxv1.ReleaseState_RELEASE_STATE_FAILED,
}

func getRelease(ctx context.Context, tx *store.Tx, id uuid.UUID) (*bohuxv1.Release, error) {
	rel, err := scanRelease(tx.QueryRow(ctx, releaseSelect+` WHERE h.id = $1 AND NOT h.deleted`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return rel, err
}

func scanRelease(row pgx.Row) (*bohuxv1.Release, error) {
	var (
		m           metaRow
		app, config uuid.UUID
		state       string
		failure     []byte
		r           bohuxv1.Release
	)
	if err := row.Scan(append(m.dest(), &app, &config, &state, &r.ArtifactDigest, &r.SourceRef,
		&r.ProvenanceRef, &r.SbomRef, &r.BuildWorkflowId, &r.ArtifactAvailable, &failure)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("read release: %w", err)
	}
	r.Meta, r.AppId, r.ConfigRevisionId, r.State = m.proto(), app.String(), config.String(), releaseStates[state]
	if failure != nil {
		r.Failure = &bohuxv1.ErrorDetail{}
		if err := loadJSON.Unmarshal(failure, r.Failure); err != nil {
			return nil, fmt.Errorf("decode release failure: %w", err)
		}
	}
	return &r, nil
}

package api

import (
	"context"
	"errors"
	"regexp"

	"github.com/google/uuid"

	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/store"
)

// slugPattern is a DNS label: lowercase letters, digits, and inner hyphens, 1–63
// characters. Slugs end up in hostnames, paths, and file names on nodes.
var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func validSlug(field, slug string) error {
	if !slugPattern.MatchString(slug) {
		return invalidArgument(field, "INVALID_SLUG",
			field+" must be 1-63 lowercase letters, digits, or inner hyphens")
	}
	return nil
}

// authorizeProject is the handler-side check (ADR-0023). A caller who cannot see the
// project at all gets ErrNotFound, so existence does not leak across scopes; one who
// can see it but lacks min gets ErrPermissionDenied.
func authorizeProject(ctx context.Context, project uuid.UUID, min auth.Role) error {
	if auth.RequireProject(ctx, project, auth.RoleViewer) != nil {
		return store.ErrNotFound
	}
	if err := auth.RequireProject(ctx, project, min); err != nil {
		return err
	}
	return nil
}

// requiredRevision parses expected_revision_id, which updates and deletes require
// (api-contract.md, convention 4).
func requiredRevision(s string) (uuid.UUID, error) {
	if s == "" {
		return uuid.Nil, invalidArgument("expected_revision_id", "REQUIRED",
			"expected_revision_id is required")
	}
	return parseID("expected_revision_id", s)
}

// errHasChildren is returned when deleting a resource that still has live children.
var errHasChildren = errors.New("resource still has live children; delete them first")

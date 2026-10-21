package provider

import "context"

// ReleaseImage is an image on the node that belongs to one Release of one App, found
// by its io.bohux.app and io.bohux.release labels.
type ReleaseImage struct {
	ID      string
	App     string
	Release string
}

// ImageStore is an optional interface for providers whose target keeps images on the
// node, such as Docker. It is not part of Provider and not a verb: image retention is
// an agent duty that runs beside reconciling (ADR-0020 item 3, ADR-0025).
type ImageStore interface {
	// ReleaseImages lists the images that carry Bohux release labels.
	ReleaseImages(ctx context.Context) ([]ReleaseImage, error)
	// ImagesInUse returns the IDs of images any container on the node uses.
	ImagesInUse(ctx context.Context) (map[string]bool, error)
	// RemoveImage deletes one image, without force.
	RemoveImage(ctx context.Context, id string) error
}

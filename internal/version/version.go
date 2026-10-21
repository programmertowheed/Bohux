// Package version reports the build version of the bohux binary.
package version

// Version is set at build time with -ldflags "-X github.com/bohumix/bohux/internal/version.Version=...".
var Version = "dev"

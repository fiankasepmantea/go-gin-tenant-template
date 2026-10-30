// Package version holds build information injected via -ldflags at compile time.
package version

// These values are overridden at build time, for example:
//
//	go build -ldflags "-X github.com/fiankasepmantea/go-gin-tenant-template/internal/version.Version=v1.2.3"
var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

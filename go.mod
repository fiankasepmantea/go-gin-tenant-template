module github.com/fiankasepmantea/go-gin-tenant-template

// Minimum Go version. The dependencies in use (gin v1.12, gorm postgres driver, pgx) require 1.25.0.
// See docs/libraries.md for the Go version policy and the library pin list.
go 1.25.0

require (
	aidanwoods.dev/go-paseto v1.6.0
	github.com/gin-gonic/gin v1.12.0
	github.com/google/uuid v1.6.0
	github.com/joho/godotenv v1.5.1
	github.com/spf13/cobra v1.10.2
	gorm.io/driver/postgres v1.6.3
	gorm.io/gorm v1.31.2
)

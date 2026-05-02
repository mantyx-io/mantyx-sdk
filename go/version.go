package mantyx

import (
	_ "embed"
	"strings"
)

//go:embed sdk-version.txt
var versionFile string

// Version returns the SDK release version (semver), aligned with the repo root
// VERSION file and the @mantyx/sdk npm package.
func Version() string {
	return strings.TrimSpace(versionFile)
}

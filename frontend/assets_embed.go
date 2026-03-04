//go:build frontend_dist

package frontendassets

import "embed"

// DistFS embeds the compiled frontend assets so release binaries can run standalone.
//
//go:embed all:dist
var DistFS embed.FS

const HasEmbeddedAssets = true

//go:build !frontend_dist

package frontendassets

import "io/fs"

var DistFS fs.FS

const HasEmbeddedAssets = false

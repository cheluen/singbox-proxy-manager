//go:build bundled_kernel

package kernel

import _ "embed"

//go:embed sing-box
var payload string

const Bundled = true

func Prepare(configDir string) (string, error) {
	return extract(configDir, payload)
}

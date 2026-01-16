package version

import (
	_ "embed"
	"strings"
)

//go:embed version.txt
var rawVersion string

func Version() string {
	v := strings.TrimSpace(rawVersion)
	if v == "" {
		return "unknown"
	}
	return v
}

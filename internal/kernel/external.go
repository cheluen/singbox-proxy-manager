//go:build !bundled_kernel

package kernel

const Bundled = false

func Prepare(string) (string, error) {
	return "", nil
}

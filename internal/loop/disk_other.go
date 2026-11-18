//go:build !linux && !darwin

package loop

import "errors"

func freeBytes(dir string) (uint64, error) {
	return 0, errors.New("free space is unavailable")
}

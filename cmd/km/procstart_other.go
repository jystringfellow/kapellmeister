//go:build !linux && !darwin

package main

import "errors"

func processStart(pid int) (string, error) {
	return "", errors.New("process identity is unavailable")
}

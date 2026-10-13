//go:build !darwin && !linux

package sandbox

import "errors"

func (s *Sandbox) start() error {
	return errors.New("the sandbox runs on macOS and Linux only")
}

func (s *Sandbox) wrap([]string, policy) []string {
	panic("sandbox: unsupported platform") // New fails first
}

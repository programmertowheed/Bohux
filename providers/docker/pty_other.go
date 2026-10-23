//go:build !linux

package docker

import (
	"errors"
	"os"
	"syscall"
)

// The agent runs on Linux; elsewhere exec has no terminal.
func openPTY() (controller, terminal *os.File, err error) {
	return nil, nil, errors.New("exec with a TTY needs a Linux agent")
}

func setSize(*os.File, uint16, uint16) error { return nil }

func ttyProcAttr() *syscall.SysProcAttr { return nil }

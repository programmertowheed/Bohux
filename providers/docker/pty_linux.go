package docker

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// openPTY returns a pseudo-terminal pair: the agent keeps the controller, the
// command gets the terminal. Linux's /dev/ptmx, unlocked and named by ioctl.
func openPTY() (controller, terminal *os.File, err error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}
	controller = os.NewFile(uintptr(fd), "/dev/ptmx")
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		_ = controller.Close()
		return nil, nil, fmt.Errorf("unlock pty: %w", err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		_ = controller.Close()
		return nil, nil, fmt.Errorf("name pty: %w", err)
	}
	name := fmt.Sprintf("/dev/pts/%d", n)
	tfd, err := unix.Open(name, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = controller.Close()
		return nil, nil, fmt.Errorf("open %s: %w", name, err)
	}
	return controller, os.NewFile(uintptr(tfd), name), nil
}

// setSize sets the terminal's window size; the command sees SIGWINCH.
func setSize(controller *os.File, rows, cols uint16) error {
	if rows == 0 || cols == 0 {
		return nil
	}
	return unix.IoctlSetWinsize(int(controller.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols})
}

// ttyProcAttr makes the command the session leader with the terminal (its stdin) as
// controlling terminal, so it gets job control and SIGWINCH.
func ttyProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
}

package swarm

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// probeInNamespace sends GET path to 127.0.0.1:port inside the network namespace of
// process pid. The socket is created on a locked OS thread that has entered the
// namespace; the thread leaves it again before it is unlocked, or, if it cannot,
// stays locked so the runtime discards it with the goroutine.
func probeInNamespace(ctx context.Context, base *http.Client, pid int, port int32, path string, timeout time.Duration) error {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
	client := &http.Client{
		CheckRedirect: base.CheckRedirect,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialInNamespace(ctx, pid, addr, timeout)
			},
		},
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(hctx, http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		return fmt.Errorf("health request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("health check: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("health check returned %d", resp.StatusCode)
	}
	return nil
}

func dialInNamespace(ctx context.Context, pid int, addr string, timeout time.Duration) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		conn, restored, err := dialLocked(pid, addr, timeout)
		if restored {
			runtime.UnlockOSThread()
		}
		done <- result{conn, err}
	}()
	select {
	case r := <-done:
		return r.conn, r.err
	case <-ctx.Done():
		go func() {
			if r := <-done; r.conn != nil {
				_ = r.conn.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

// dialLocked runs on a locked thread; restored is false when the thread could not
// return to its own namespace and must not run anything else.
func dialLocked(pid int, addr string, timeout time.Duration) (net.Conn, bool, error) {
	own, err := unix.Open("/proc/thread-self/ns/net", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, true, fmt.Errorf("open own network namespace: %w", err)
	}
	defer func() { _ = unix.Close(own) }()
	target, err := unix.Open(fmt.Sprintf("/proc/%d/ns/net", pid), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, true, fmt.Errorf("open the task's network namespace: %w", err)
	}
	defer func() { _ = unix.Close(target) }()
	if err := unix.Setns(target, unix.CLONE_NEWNET); err != nil {
		return nil, true, fmt.Errorf("enter the task's network namespace: %w", err)
	}
	// The socket belongs to the namespace it is created in; it keeps working after
	// the thread leaves.
	conn, dialErr := net.DialTimeout("tcp", addr, timeout)
	if err := unix.Setns(own, unix.CLONE_NEWNET); err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, false, fmt.Errorf("leave the task's network namespace: %w", err)
	}
	if dialErr != nil {
		return nil, true, fmt.Errorf("dial %s in the task: %w", addr, dialErr)
	}
	return conn, true, nil
}

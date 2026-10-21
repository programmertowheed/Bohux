//go:build integration

package controlplane

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/bohumix/bohux/internal/store/storetest"
)

func TestServeMigratesServesAndShutsDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ln, Config{DatabaseURL: storetest.NewAppDSN(t)})
	}()

	url := "http://" + ln.Addr().String() + "/healthz"
	var resp *http.Response
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err = http.Get(url)
		if err == nil && resp.StatusCode == http.StatusOK {
			break
		}
		if err == nil {
			_ = resp.Body.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("healthz never became ready: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "ok\n" {
		t.Errorf("healthz body = %q", body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve after cancel: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

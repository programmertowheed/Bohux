package controlplane

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

func TestHealthz(t *testing.T) {
	tests := []struct {
		name string
		db   fakeDB
		want int
	}{
		{name: "database up", db: fakeDB{}, want: http.StatusOK},
		{name: "database down", db: fakeDB{err: errors.New("down")}, want: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			newMux(tt.db, func(*http.ServeMux) {}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			if rec.Code != tt.want {
				t.Errorf("status %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestRunRequiresDatabaseURL(t *testing.T) {
	if err := Run(context.Background(), Config{ListenAddr: "127.0.0.1:0"}); err == nil {
		t.Fatal("Run without a database URL: got nil error")
	}
}

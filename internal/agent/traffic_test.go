package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/bohumix/bohux/internal/slo"
)

func accessJSON(upstream string, status int, seconds float64) string {
	return fmt.Sprintf(`{"level":"info","logger":"http.log.access.bohux","status":%d,"duration":%g,"upstream":%q}`+"\n",
		status, seconds, upstream)
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTraffic(t *testing.T) {
	const a, b = "app-a:8080", "app-b:8080"
	tests := []struct {
		name string
		// before is the file's content at the agent's first poll, unless noFile.
		before string
		noFile bool
		// steps write to the file and poll; path is the log, tr the tailer.
		steps func(t *testing.T, path string, tr *Traffic)
		want  map[string][2]uint64 // upstream: requests, server errors
	}{
		{
			name:   "a file present at start is read from its end",
			before: accessJSON(a, 500, 0.2),
			steps: func(t *testing.T, path string, tr *Traffic) {
				appendTo(t, path, accessJSON(a, 200, 0.2)+accessJSON(a, 502, 0.2))
				tr.poll()
			},
			want: map[string][2]uint64{a: {2, 1}},
		},
		{
			name:   "a file created later is read from its start",
			noFile: true,
			steps: func(t *testing.T, path string, tr *Traffic) {
				appendTo(t, path, accessJSON(a, 200, 0.2))
				tr.poll()
			},
			want: map[string][2]uint64{a: {1, 0}},
		},
		{
			name: "a partial line waits for its end",
			steps: func(t *testing.T, path string, tr *Traffic) {
				line := accessJSON(b, 503, 0.2)
				appendTo(t, path, line[:10])
				tr.poll()
				if c := tr.Counts(b); c.Requests != 0 {
					t.Errorf("half a line counted: %+v", c)
				}
				appendTo(t, path, line[10:])
				tr.poll()
			},
			want: map[string][2]uint64{b: {1, 1}},
		},
		{
			name: "a rolled file is read to its end, then the new one from its start",
			steps: func(t *testing.T, path string, tr *Traffic) {
				appendTo(t, path, accessJSON(b, 200, 0.2))
				if err := os.Rename(path, path+".1"); err != nil {
					t.Fatal(err)
				}
				appendTo(t, path, accessJSON(b, 500, 0.2))
				tr.poll()
				appendTo(t, path, accessJSON(b, 200, 0.2))
				tr.poll()
			},
			want: map[string][2]uint64{b: {3, 1}},
		},
		{
			name:   "a truncated file is read from its start",
			before: accessJSON(a, 200, 0.2),
			steps: func(t *testing.T, path string, tr *Traffic) {
				appendTo(t, path, accessJSON(a, 200, 0.2))
				tr.poll()
				if err := os.Truncate(path, 0); err != nil {
					t.Fatal(err)
				}
				tr.poll()
				appendTo(t, path, accessJSON(a, 500, 0.2))
				tr.poll()
			},
			want: map[string][2]uint64{a: {2, 1}},
		},
		{
			name: "lines without an upstream or not JSON are skipped",
			steps: func(t *testing.T, path string, tr *Traffic) {
				appendTo(t, path, "not json\n"+`{"status":500,"duration":1}`+"\n"+accessJSON(a, 200, 0.2))
				tr.poll()
			},
			want: map[string][2]uint64{a: {1, 0}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "access.log")
			if !tt.noFile {
				appendTo(t, path, tt.before)
			}
			tr := &Traffic{Path: path}
			tr.poll()
			tt.steps(t, path, tr)
			for up, w := range tt.want {
				c := tr.Counts(up)
				if got := [2]uint64{c.Requests, c.ServerErrors}; got != w {
					t.Errorf("%s: got %v, want %v", up, got, w)
				}
			}
		})
	}
}

func TestTrafficCountsSumsEndpoints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	tr := &Traffic{Path: path}
	tr.poll()
	appendTo(t, path, accessJSON("x:1", 200, 0.004)+accessJSON("y:1", 500, 20)+accessJSON("x:1", 200, 0.2))
	tr.poll()
	c := tr.Counts("x:1", "y:1", "z:1")
	if c.Requests != 3 || c.ServerErrors != 1 || c.Latency[0] != 1 || c.Latency[slo.Bucket(200)] != 1 ||
		c.Latency[len(c.Latency)-1] != 1 {
		t.Errorf("got %+v", c)
	}
	if c := tr.Counts("z:1"); c.Requests != 0 || len(c.Latency) != len(slo.LatencyBoundsMS)+1 {
		t.Errorf("unknown endpoint: %+v", c)
	}
}

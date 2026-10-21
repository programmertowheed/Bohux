package main

import (
	"bytes"
	"errors"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr error
	}{
		{name: "no args", args: nil, wantErr: errUsage},
		{name: "version", args: []string{"version"}, want: "dev\n"},
		{name: "unknown command", args: []string{"nope"}, wantErr: errUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := run(tt.args, &out)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("run(%v) error = %v, want %v", tt.args, err, tt.wantErr)
			}
			if got := out.String(); got != tt.want {
				t.Errorf("run(%v) output = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

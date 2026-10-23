package version

import "testing"

func TestCheckAgent(t *testing.T) {
	tests := []struct {
		name         string
		cp, agent    string
		protocol     uint32
		want         bool
		wantMinAgent string
	}{
		{"same version", "0.3.1", "0.3.0", Protocol, true, "0.2.0"},
		{"one minor behind", "0.3.1", "v0.2.9", Protocol, true, "0.2.0"},
		{"two minors behind", "0.3.1", "0.1.9", Protocol, false, "0.2.0"},
		{"newer agent", "0.3.1", "0.4.0", Protocol, false, "0.2.0"},
		{"other major", "1.0.0", "0.9.0", Protocol, false, "1.0.0"},
		{"pre-release agent", "0.3.0", "0.3.0-rc.1", Protocol, true, "0.2.0"},
		{"dev agent, release control plane", "0.3.0", "dev", Protocol, false, "0.2.0"},
		{"dev control plane accepts any release", "dev", "0.1.0", Protocol, true, ""},
		{"unknown protocol", "0.3.0", "0.3.0", Protocol + 1, false, "0.2.0"},
		{"zero protocol", "0.3.0", "0.3.0", 0, false, "0.2.0"},
		{"minor zero has no previous minor", "1.0.0", "1.0.0", Protocol, true, "1.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckAgent(tt.cp, tt.agent, tt.protocol)
			if got.OK != tt.want {
				t.Errorf("OK = %v (%s), want %v", got.OK, got.Reason, tt.want)
			}
			if !got.OK && got.Reason == "" {
				t.Error("refusal without a reason")
			}
			if got.MinAgentVersion != tt.wantMinAgent {
				t.Errorf("MinAgentVersion = %q, want %q", got.MinAgentVersion, tt.wantMinAgent)
			}
		})
	}
}

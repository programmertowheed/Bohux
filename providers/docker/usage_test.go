package docker

import "testing"

func TestUsageParsing(t *testing.T) {
	for in, want := range map[string]int64{"524KiB": 524 << 10, "1.5GiB": 3 << 29, "12MB": 12e6, "0B": 0, "junk": 0} {
		if got := byteSize(in); got != want {
			t.Errorf("byteSize(%q) = %d, want %d", in, got, want)
		}
	}
	for in, want := range map[string]int64{"12.34%": 123, "0.00%": 0, "250%": 2500, "": 0} {
		if got := cpuMillicores(in); got != want {
			t.Errorf("cpuMillicores(%q) = %d, want %d", in, got, want)
		}
	}
	if slotOf("bohux-api-prod-1234abcd-green") != "green" || slotOf("bohux-api-prod-1234abcd") != "" {
		t.Error("slotOf")
	}
}

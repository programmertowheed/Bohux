package docker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"path"
	"regexp"
)

// options is what this provider accepts in Spec.Raw (ADR-0032). Both name things that
// already exist on the node; the provider never creates or deletes them.
type options struct {
	// Networks are external Docker networks the workload joins besides its own.
	Networks []string `json:"networks"`
	// Volumes mount external named volumes; host paths are not accepted.
	Volumes []volumeOption `json:"volumes"`
	// PublishAddress is the host IP published ports bind to; empty means all.
	PublishAddress string `json:"publish_address"`
}

type volumeOption struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// dockerName is what Docker accepts for network and volume names.
var dockerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// parseOptions decodes and checks Spec.Raw; unknown keys are refused.
func parseOptions(raw []byte) (options, error) {
	var o options
	if len(raw) == 0 {
		return o, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return options{}, fmt.Errorf("provider options: %w", err)
	}
	for _, n := range o.Networks {
		if !dockerName.MatchString(n) || n == "default" {
			return options{}, fmt.Errorf("provider options: %q is not a network name", n)
		}
	}
	if o.PublishAddress != "" {
		if ip := net.ParseIP(o.PublishAddress); ip == nil {
			return options{}, fmt.Errorf("provider options: publish_address %q is not an IP address", o.PublishAddress)
		}
	}
	for _, v := range o.Volumes {
		if !dockerName.MatchString(v.Source) {
			return options{}, fmt.Errorf("provider options: %q is not a volume name (host paths are not accepted)", v.Source)
		}
		if !path.IsAbs(v.Target) || path.Clean(v.Target) != v.Target || v.Target == "/" {
			return options{}, fmt.Errorf("provider options: volume target %q must be a clean absolute path", v.Target)
		}
	}
	return o, nil
}

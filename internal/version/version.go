// Package version reports the build version of the bohux binary and the agent
// protocol version, and decides which agents a control plane accepts (ADR-0020 item 8).
package version

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is set at build time with -ldflags "-X github.com/bohumix/bohux/internal/version.Version=...".
var Version = "dev"

// Protocol is the bohux.agent.v1 protocol version this binary speaks. The control
// plane accepts Protocol and Protocol-1 (ADR-0003).
const Protocol uint32 = 1

// Release is a parsed MAJOR.MINOR.PATCH version, with an optional leading "v".
type Release struct {
	Major, Minor, Patch int
}

// Parse reads a release version. "dev" and other non-release strings fail.
func Parse(s string) (Release, error) {
	parts := strings.SplitN(strings.TrimPrefix(s, "v"), ".", 3)
	if len(parts) != 3 {
		return Release{}, fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", s)
	}
	// Drop pre-release and build suffixes from the patch: 1.2.3-rc.1+abc.
	if i := strings.IndexAny(parts[2], "-+"); i >= 0 {
		parts[2] = parts[2][:i]
	}
	var nums [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Release{}, fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", s)
		}
		nums[i] = n
	}
	return Release{Major: nums[0], Minor: nums[1], Patch: nums[2]}, nil
}

// Skew is the outcome of checking an agent against a control plane.
type Skew struct {
	OK bool
	// Why the agent was refused, for VersionRejected.message.
	Reason string
	// The oldest agent version and protocol the control plane accepts.
	MinAgentVersion string
	MinProtocol     uint32
}

// CheckAgent applies the skew policy: same major, agent minor equal to the control
// plane's or one behind, protocol current or one behind. A newer agent is refused too:
// upgrade the control plane first. A "dev" control plane accepts any agent, for local
// development; a "dev" agent is refused by a release control plane.
func CheckAgent(controlPlane, agent string, agentProtocol uint32) Skew {
	minProto := Protocol
	if minProto > 1 {
		minProto--
	}
	cp, err := Parse(controlPlane)
	if err != nil {
		s := Skew{OK: agentProtocol >= minProto && agentProtocol <= Protocol, MinProtocol: minProto}
		if !s.OK {
			s.Reason = fmt.Sprintf("protocol %d unsupported", agentProtocol)
		}
		return s
	}
	minMinor := cp.Minor
	if minMinor > 0 {
		minMinor--
	}
	s := Skew{MinAgentVersion: fmt.Sprintf("%d.%d.0", cp.Major, minMinor), MinProtocol: minProto}
	if agentProtocol < minProto || agentProtocol > Protocol {
		s.Reason = fmt.Sprintf("protocol %d unsupported; this control plane speaks %d and %d",
			agentProtocol, minProto, Protocol)
		return s
	}
	a, err := Parse(agent)
	switch {
	case err != nil:
		s.Reason = fmt.Sprintf("agent version %q is not a release", agent)
	case a.Major != cp.Major || a.Minor > cp.Minor:
		s.Reason = fmt.Sprintf("agent %s is newer than control plane %s; upgrade the control plane first",
			agent, controlPlane)
	case a.Minor < minMinor:
		s.Reason = fmt.Sprintf("agent %s is older than %s; upgrade the agent", agent, s.MinAgentVersion)
	default:
		s.OK = true
	}
	return s
}

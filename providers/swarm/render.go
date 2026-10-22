package swarm

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/provider"
	"github.com/bohumix/bohux/providers/docker"
)

// stackHeader tells a reader what the file is and how to run it without Bohux.
const stackHeader = `# Written by Bohux. This file is the complete definition of this workload:
# without Bohux, run it with
#   docker stack deploy -c stack.yaml --resolve-image never %s
# Secret values are not here; they are in secrets.env (mode 0600).
`

type stackDoc struct {
	Services map[string]stackService  `yaml:"services"`
	Networks map[string]stackExternal `yaml:"networks,omitempty"`
	Volumes  map[string]stackExternal `yaml:"volumes,omitempty"`
}

type stackExternal struct {
	External bool `yaml:"external"`
}

type stackService struct {
	Image   string   `yaml:"image"`
	EnvFile []string `yaml:"env_file"`
	// Labels are the containers'; the spec hash is read back from each task.
	Labels   map[string]string       `yaml:"labels"`
	Ports    []stackPort             `yaml:"ports,omitempty"`
	Networks map[string]*stackAttach `yaml:"networks"`
	Volumes  []string                `yaml:"volumes,omitempty"`
	Logging  stackLogging            `yaml:"logging"`
	Deploy   stackDeploy             `yaml:"deploy"`
}

type stackAttach struct {
	Aliases []string `yaml:"aliases,omitempty"`
}

// stackPort is the long port syntax; host mode, since dnsrr refuses the routing mesh.
type stackPort struct {
	Target    int32  `yaml:"target"`
	Published int32  `yaml:"published"`
	Protocol  string `yaml:"protocol"`
	Mode      string `yaml:"mode"`
}

type stackLogging struct {
	Driver  string            `yaml:"driver"`
	Options map[string]string `yaml:"options"`
}

type stackDeploy struct {
	Replicas      int32             `yaml:"replicas"`
	EndpointMode  string            `yaml:"endpoint_mode"`
	Labels        map[string]string `yaml:"labels"`
	RestartPolicy stackRestart      `yaml:"restart_policy"`
	Resources     *stackResources   `yaml:"resources,omitempty"`
}

type stackRestart struct {
	Condition string `yaml:"condition"`
}

type stackResources struct {
	Limits stackLimits `yaml:"limits"`
}

type stackLimits struct {
	CPUs   string `yaml:"cpus,omitempty"`
	Memory string `yaml:"memory,omitempty"`
}

// state is what Status needs that Swarm does not record: the desired replica count,
// health check, and ingress ports. Written before the deploy.
type state struct {
	Replicas     int32       `json:"replicas"`
	Health       *healthJSON `json:"health,omitempty"`
	IngressPorts []int32     `json:"ingress_ports,omitempty"`
}

type healthJSON struct {
	Path           string `json:"path"`
	Port           int32  `json:"port"`
	TimeoutSeconds int32  `json:"timeout_seconds,omitempty"`
}

// render produces stack.yaml, secrets.env, and the state file for spec. Secret values
// go only into secrets.env (ADR-0008).
func render(spec provider.Spec) (stackYAML, secretsEnv, stateJSON []byte, err error) {
	rt := spec.Runtime
	if rt == nil {
		return nil, nil, nil, errors.New("spec has no runtime")
	}
	if spec.Image == "" {
		return nil, nil, nil, errors.New("spec has no image")
	}
	opts, err := parseOptions(spec.Raw)
	if err != nil {
		return nil, nil, nil, err
	}
	name := stack(spec.Ref)
	svc := stackService{
		Image: spec.Image,
		// Always referenced, so adding a first secret later is only a content change.
		EnvFile: []string{secretsFile},
		Labels: map[string]string{
			labelApp: spec.Ref.App, labelEnvironment: spec.Ref.Environment,
			labelRelease: spec.Release, labelSpecHash: spec.Hash,
		},
		Networks: map[string]*stackAttach{"default": nil},
		// ADR-0020 item 3: bounded container logs.
		Logging: stackLogging{Driver: "json-file", Options: map[string]string{"max-size": "10m", "max-file": "3"}},
		Deploy: stackDeploy{
			Replicas: rt.GetReplicas(), EndpointMode: "dnsrr",
			Labels:        map[string]string{labelApp: spec.Ref.App, labelEnvironment: spec.Ref.Environment},
			RestartPolicy: stackRestart{Condition: "any"},
		},
	}
	for _, port := range rt.GetPorts() {
		if port.GetHostPort() == 0 {
			continue
		}
		proto := "tcp"
		if port.GetProtocol() == bohuxv1.PortProtocol_PORT_PROTOCOL_UDP {
			proto = "udp"
		}
		svc.Ports = append(svc.Ports, stackPort{Target: port.GetContainerPort(), Published: port.GetHostPort(),
			Protocol: proto, Mode: "host"})
	}
	if rt.GetCpu() != "" || rt.GetMemory() != "" {
		cpus, err := docker.CPULimit(rt.GetCpu())
		if err != nil {
			return nil, nil, nil, err
		}
		mem, err := docker.MemoryLimit(rt.GetMemory())
		if err != nil {
			return nil, nil, nil, err
		}
		svc.Deploy.Resources = &stackResources{Limits: stackLimits{CPUs: cpus, Memory: mem}}
	}

	var env strings.Builder
	env.WriteString("# Written by Bohux. Secret values for this workload. Keep mode 0600.\n")
	if spec.Secrets != nil {
		values, err := spec.Secrets.Open()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("open secrets: %w", err)
		}
		keys := make([]string, 0, len(values))
		for k := range values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if strings.ContainsAny(values[k], "\n\r") {
				// The value itself must not appear in the error (rule 3).
				return nil, nil, nil, fmt.Errorf("secret %s contains a newline, which env files cannot hold", k)
			}
			env.WriteString(k + "=" + values[k] + "\n")
		}
	}

	doc := stackDoc{Services: map[string]stackService{}}
	if spec.Ingress || len(opts.Networks) > 0 {
		doc.Networks = map[string]stackExternal{}
	}
	if spec.Ingress {
		// The ingress dials the stack name; with dnsrr Swarm's DNS answers with every
		// task, as Docker's does for the Docker provider's alias.
		svc.Networks[IngressNetwork] = &stackAttach{Aliases: []string{name}}
		doc.Networks[IngressNetwork] = stackExternal{External: true}
	}
	for _, n := range opts.Networks {
		svc.Networks[n] = nil
		doc.Networks[n] = stackExternal{External: true}
	}
	if len(opts.Volumes) > 0 {
		doc.Volumes = map[string]stackExternal{}
		for _, v := range opts.Volumes {
			svc.Volumes = append(svc.Volumes, v.Source+":"+v.Target)
			doc.Volumes[v.Source] = stackExternal{External: true}
		}
	}
	doc.Services[service] = svc
	body, err := yaml.Marshal(doc)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("render stack file: %w", err)
	}
	st := state{Replicas: rt.GetReplicas()}
	if spec.Ingress {
		for _, port := range rt.GetPorts() {
			st.IngressPorts = append(st.IngressPorts, port.GetContainerPort())
		}
	}
	if h := rt.GetHealth(); h != nil {
		st.Health = &healthJSON{Path: h.GetPath(), Port: h.GetPort(), TimeoutSeconds: h.GetTimeoutSeconds()}
	}
	stJSON, err := json.Marshal(st)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("render state: %w", err)
	}
	return append([]byte(fmt.Sprintf(stackHeader, name)), body...), []byte(env.String()), stJSON, nil
}

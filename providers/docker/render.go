package docker

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/provider"
)

// composeHeader tells a reader what the file is and how to run it without Bohux.
const composeHeader = `# Written by Bohux. This file is the complete definition of this workload:
# without Bohux, run it with
#   docker compose -p %s -f compose.yaml up -d
# Secret values are not here; they are in secrets.env (mode 0600).
`

type composeDoc struct {
	Name     string                     `yaml:"name"`
	Services map[string]composeService  `yaml:"services"`
	Networks map[string]composeExternal `yaml:"networks,omitempty"`
	Volumes  map[string]composeExternal `yaml:"volumes,omitempty"`
}

// composeExternal declares a network or volume that exists outside this project.
type composeExternal struct {
	External bool `yaml:"external"`
}

type composeService struct {
	Image   string   `yaml:"image"`
	Restart string   `yaml:"restart"`
	EnvFile []string `yaml:"env_file,omitempty"`
	Ports   []string `yaml:"ports,omitempty"`
	// Networks is a list of names, or, with the ingress, names to attachments (the
	// long form is the only one that sets an alias).
	Networks any               `yaml:"networks,omitempty"`
	Volumes  []string          `yaml:"volumes,omitempty"`
	Labels   map[string]string `yaml:"labels"`
	Logging  composeLogging    `yaml:"logging"`
	Deploy   composeDeploy     `yaml:"deploy"`
}

type composeAttach struct {
	Aliases []string `yaml:"aliases,omitempty"`
}

type composeLogging struct {
	Driver  string            `yaml:"driver"`
	Options map[string]string `yaml:"options"`
}

type composeDeploy struct {
	Replicas  int32             `yaml:"replicas"`
	Resources *composeResources `yaml:"resources,omitempty"`
}

type composeResources struct {
	Limits composeLimits `yaml:"limits"`
}

type composeLimits struct {
	CPUs   string `yaml:"cpus,omitempty"`
	Memory string `yaml:"memory,omitempty"`
}

// state is what Status needs that Docker does not record: the desired replica count
// and health check. Written before `up`, so it describes what is being applied.
type state struct {
	Replicas int32       `json:"replicas"`
	Health   *healthJSON `json:"health,omitempty"`
	// Ports the ingress reaches, when the workload has one (ADR-0034).
	IngressPorts []int32 `json:"ingress_ports,omitempty"`
}

type healthJSON struct {
	Path           string `json:"path"`
	Port           int32  `json:"port"`
	TimeoutSeconds int32  `json:"timeout_seconds,omitempty"`
}

// render produces compose.yaml, secrets.env, and the state file for spec. Secret values
// go only into secrets.env (ADR-0008).
func render(spec provider.Spec) (composeYAML, secretsEnv, stateJSON []byte, err error) {
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
	name := project(spec.Ref)
	svc := composeService{
		Image:   spec.Image,
		Restart: "unless-stopped",
		Labels: map[string]string{
			labelApp: spec.Ref.App, labelEnvironment: spec.Ref.Environment,
			labelRelease: spec.Release, labelSpecHash: spec.Hash,
		},
		// ADR-0020 item 3: bounded container logs.
		Logging: composeLogging{Driver: "json-file", Options: map[string]string{"max-size": "10m", "max-file": "3"}},
		Deploy:  composeDeploy{Replicas: rt.GetReplicas()},
	}
	for _, port := range rt.GetPorts() {
		if port.GetHostPort() == 0 {
			continue
		}
		proto := "tcp"
		if port.GetProtocol() == bohuxv1.PortProtocol_PORT_PROTOCOL_UDP {
			proto = "udp"
		}
		published := fmt.Sprintf("%d:%d/%s", port.GetHostPort(), port.GetContainerPort(), proto)
		if opts.PublishAddress != "" {
			published = net.JoinHostPort(opts.PublishAddress, strconv.Itoa(int(port.GetHostPort()))) +
				fmt.Sprintf(":%d/%s", port.GetContainerPort(), proto)
		}
		svc.Ports = append(svc.Ports, published)
	}
	if rt.GetCpu() != "" || rt.GetMemory() != "" {
		cpus, err := cpuLimit(rt.GetCpu())
		if err != nil {
			return nil, nil, nil, err
		}
		mem, err := memoryLimit(rt.GetMemory())
		if err != nil {
			return nil, nil, nil, err
		}
		svc.Deploy.Resources = &composeResources{Limits: composeLimits{CPUs: cpus, Memory: mem}}
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
	// Always referenced, so adding a first secret later is only a content change.
	svc.EnvFile = []string{secretsFile}

	doc := composeDoc{Name: name, Services: map[string]composeService{service: svc}}
	switch {
	case spec.Ingress:
		// The ingress dials the project name; Docker's DNS answers with every replica.
		attach := map[string]composeAttach{"default": {}, IngressNetwork: {Aliases: []string{name}}}
		doc.Networks = map[string]composeExternal{IngressNetwork: {External: true}}
		for _, n := range opts.Networks {
			attach[n] = composeAttach{}
			doc.Networks[n] = composeExternal{External: true}
		}
		svc.Networks = attach
	case len(opts.Networks) > 0:
		// Listing networks replaces the default one, so it is listed too.
		svc.Networks = append([]string{"default"}, opts.Networks...)
		doc.Networks = map[string]composeExternal{}
		for _, n := range opts.Networks {
			doc.Networks[n] = composeExternal{External: true}
		}
	}
	if len(opts.Volumes) > 0 {
		doc.Volumes = map[string]composeExternal{}
		for _, v := range opts.Volumes {
			svc.Volumes = append(svc.Volumes, v.Source+":"+v.Target)
			doc.Volumes[v.Source] = composeExternal{External: true}
		}
	}
	doc.Services[service] = svc
	body, err := yaml.Marshal(doc)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("render compose file: %w", err)
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
	return append([]byte(fmt.Sprintf(composeHeader, name)), body...), []byte(env.String()), stJSON, nil
}

// cpuLimit turns a Kubernetes quantity ("500m", "2") into Compose cpus ("0.5", "2").
func cpuLimit(q string) (string, error) {
	if q == "" {
		return "", nil
	}
	if m, ok := strings.CutSuffix(q, "m"); ok {
		n, err := strconv.Atoi(m)
		if err != nil || n <= 0 {
			return "", fmt.Errorf("cpu %q is not a quantity", q)
		}
		return strconv.FormatFloat(float64(n)/1000, 'f', -1, 64), nil
	}
	f, err := strconv.ParseFloat(q, 64)
	if err != nil || f <= 0 {
		return "", fmt.Errorf("cpu %q is not a quantity", q)
	}
	return q, nil
}

// memoryLimit turns a Kubernetes quantity ("512Mi", "1Gi", "1000000") into Compose
// bytes notation ("512m", "1g", "1000000b").
func memoryLimit(q string) (string, error) {
	if q == "" {
		return "", nil
	}
	for _, s := range []struct{ k8s, compose string }{{"Ki", "k"}, {"Mi", "m"}, {"Gi", "g"}, {"Ti", "t"}} {
		if n, ok := strings.CutSuffix(q, s.k8s); ok {
			if v, err := strconv.Atoi(n); err != nil || v <= 0 {
				return "", fmt.Errorf("memory %q is not a quantity", q)
			}
			return n + s.compose, nil
		}
	}
	if v, err := strconv.Atoi(q); err == nil && v > 0 {
		return q + "b", nil
	}
	return "", fmt.Errorf("memory %q is not a quantity (use Ki, Mi, Gi, or bytes)", q)
}

func jsonMarshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	return b, nil
}

package swarm

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/provider"
)

type values map[string]string

func (v values) Open() (map[string]string, error) { return v, nil }

var ref = provider.ResourceRef{Environment: "env-id", App: "app-id", EnvironmentSlug: "Prod", AppSlug: "api"}

func spec() provider.Spec {
	return provider.Spec{Ref: ref, Release: "rel-1", Image: "ghcr.io/acme/api@sha256:ab", Hash: "h1",
		Secrets: values{"DATABASE_URL": "postgres://u:hunter2@db/app"},
		Runtime: &bohuxv1.Runtime{Replicas: 2, Cpu: "500m", Memory: "256Mi",
			Ports:  []*bohuxv1.Port{{ContainerPort: 8080, HostPort: 80}, {ContainerPort: 9090}},
			Health: &bohuxv1.HealthCheck{Path: "/healthz", Port: 8080}}}
}

func TestRender(t *testing.T) {
	sp := spec()
	sp.Ingress = true
	stackYAML, env, stateJSON, err := render(sp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stackYAML), "hunter2") || strings.Contains(string(stateJSON), "hunter2") {
		t.Fatal("secret value outside secrets.env")
	}
	if !strings.Contains(string(env), "DATABASE_URL=postgres://u:hunter2@db/app\n") {
		t.Errorf("secrets.env = %q", env)
	}
	if !strings.HasPrefix(string(stackYAML), "# Written by Bohux") ||
		!strings.Contains(string(stackYAML), "docker stack deploy -c stack.yaml --resolve-image never "+stack(ref)) {
		t.Errorf("stack.yaml lacks the header saying how to run it:\n%s", stackYAML)
	}
	var doc stackDoc
	if err := yaml.Unmarshal(stackYAML, &doc); err != nil {
		t.Fatalf("stack.yaml does not parse: %v", err)
	}
	svc := doc.Services[service]
	if svc.Image != sp.Image || svc.Deploy.Replicas != 2 || svc.Deploy.EndpointMode != "dnsrr" ||
		svc.Deploy.Resources.Limits.CPUs != "0.5" || svc.Deploy.Resources.Limits.Memory != "256m" ||
		svc.Labels[labelSpecHash] != "h1" || svc.Labels[labelRelease] != "rel-1" ||
		svc.Logging.Options["max-size"] != "10m" || svc.Deploy.RestartPolicy.Condition != "any" {
		t.Errorf("service = %+v", svc)
	}
	// Only host ports are published, in host mode (dnsrr refuses the routing mesh).
	if len(svc.Ports) != 1 || svc.Ports[0] != (stackPort{Target: 8080, Published: 80, Protocol: "tcp", Mode: "host"}) {
		t.Errorf("ports = %+v", svc.Ports)
	}
	if a := svc.Networks[IngressNetwork]; a == nil || len(a.Aliases) != 1 || a.Aliases[0] != stack(ref) ||
		!doc.Networks[IngressNetwork].External {
		t.Errorf("ingress attachment = %+v, networks %+v", a, doc.Networks)
	}
	if !strings.Contains(string(stateJSON), `"ingress_ports":[8080,9090]`) || !strings.Contains(string(stateJSON), `"/healthz"`) {
		t.Errorf("state = %s", stateJSON)
	}

	sp.Ingress = false
	stackYAML, _, _, err = render(sp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stackYAML), IngressNetwork) {
		t.Errorf("a workload without ingress joins %s:\n%s", IngressNetwork, stackYAML)
	}
}

func TestRenderRefuses(t *testing.T) {
	for name, mutate := range map[string]func(*provider.Spec){
		"no runtime":       func(s *provider.Spec) { s.Runtime = nil },
		"no image":         func(s *provider.Spec) { s.Image = "" },
		"bad cpu":          func(s *provider.Spec) { s.Runtime.Cpu = "lots" },
		"newline secret":   func(s *provider.Spec) { s.Secrets = values{"K": "a\nb"} },
		"publish_address":  func(s *provider.Spec) { s.Raw = []byte(`{"publish_address":"127.0.0.1"}`) },
		"host path volume": func(s *provider.Spec) { s.Raw = []byte(`{"volumes":[{"source":"/etc","target":"/x"}]}`) },
		"ingress network":  func(s *provider.Spec) { s.Raw = []byte(`{"networks":["bohux-ingress"]}`) },
	} {
		sp := spec()
		mutate(&sp)
		_, _, _, err := render(sp)
		if err == nil {
			t.Errorf("%s: rendered", name)
		} else if strings.Contains(err.Error(), "a\nb") {
			t.Errorf("%s: the error quotes the secret", name)
		}
	}
}

func TestRenderOptions(t *testing.T) {
	sp := spec()
	sp.Raw = []byte(`{"networks":["shared"],"volumes":[{"source":"data","target":"/var/lib/app"}]}`)
	stackYAML, _, _, err := render(sp)
	if err != nil {
		t.Fatal(err)
	}
	var doc stackDoc
	if err := yaml.Unmarshal(stackYAML, &doc); err != nil {
		t.Fatal(err)
	}
	svc := doc.Services[service]
	if _, ok := svc.Networks["shared"]; !ok || !doc.Networks["shared"].External {
		t.Errorf("networks = %+v / %+v", svc.Networks, doc.Networks)
	}
	if len(svc.Volumes) != 1 || svc.Volumes[0] != "data:/var/lib/app" || !doc.Volumes["data"].External {
		t.Errorf("volumes = %+v / %+v", svc.Volumes, doc.Volumes)
	}
}

func TestStackName(t *testing.T) {
	long := provider.ResourceRef{Environment: "e", App: "a", Slot: "green",
		EnvironmentSlug: strings.Repeat("environment", 5), AppSlug: strings.Repeat("application-", 5)}
	for _, r := range []provider.ResourceRef{ref, long} {
		if n := serviceName(r); len(n) > 63 {
			t.Errorf("service name %q is %d characters, over Swarm's 63", n, len(n))
		}
	}
	blue, green := ref, ref
	blue.Slot, green.Slot = "blue", "green"
	if stack(blue) == stack(green) || stack(blue) == stack(ref) {
		t.Error("slots share a stack name")
	}
	if !strings.HasPrefix(stack(ref), "bohux-api-prod-") {
		t.Errorf("stack(ref) = %q", stack(ref))
	}
}

func TestParseLogLine(t *testing.T) {
	line, ok := parseLogLine("2026-10-22T11:13:47.662166872Z bohux-api-prod-1a2b3c4d_app.2@vm    | started r1")
	want := provider.LogLine{Time: time.Date(2026, 10, 22, 11, 13, 47, 662166872, time.UTC),
		Instance: "bohux-api-prod-1a2b3c4d_app.2", Message: "started r1"}
	if !ok || line != want {
		t.Errorf("parsed %+v, %v; want %+v", line, ok, want)
	}
	if _, ok := parseLogLine("error from daemon"); ok {
		t.Error("a line without a timestamp parsed")
	}
}

func TestContainerInstance(t *testing.T) {
	c := container{Labels: "a=b," + labelTask + "=bohux-api-prod-1a2b3c4d_app.1.x7k2n9,c=d"}
	if got := c.instance(); got != "bohux-api-prod-1a2b3c4d_app.1" {
		t.Errorf("instance = %q", got)
	}
}

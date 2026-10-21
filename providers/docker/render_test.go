package docker

import (
	"fmt"
	"strings"
	"testing"

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
			Ports: []*bohuxv1.Port{{ContainerPort: 8080, HostPort: 80}, {ContainerPort: 9090}}}}
}

func TestRender(t *testing.T) {
	composeYAML, env, _, err := render(spec())
	if err != nil {
		t.Fatal(err)
	}
	c := string(composeYAML)
	if strings.Contains(c, "hunter2") {
		t.Fatal("secret value in compose.yaml")
	}
	if !strings.Contains(string(env), "DATABASE_URL=postgres://u:hunter2@db/app\n") {
		t.Errorf("secrets.env = %q", env)
	}
	var doc composeDoc
	if err := yaml.Unmarshal(composeYAML, &doc); err != nil {
		t.Fatalf("compose.yaml does not parse: %v", err)
	}
	svc := doc.Services[service]
	want := map[string]string{
		"name": doc.Name, "image": svc.Image, "ports": strings.Join(svc.Ports, ","),
		"cpus": svc.Deploy.Resources.Limits.CPUs, "memory": svc.Deploy.Resources.Limits.Memory,
		"hash": svc.Labels[labelSpecHash], "release": svc.Labels[labelRelease],
		"log max-size": svc.Logging.Options["max-size"], "restart": svc.Restart,
	}
	expect := map[string]string{
		"name": project(ref), "image": "ghcr.io/acme/api@sha256:ab", "ports": "80:8080/tcp",
		"cpus": "0.5", "memory": "256m", "hash": "h1", "release": "rel-1",
		"log max-size": "10m", "restart": "unless-stopped",
	}
	for k, v := range expect {
		if want[k] != v {
			t.Errorf("%s = %q, want %q", k, want[k], v)
		}
	}
	if svc.Deploy.Replicas != 2 || len(svc.EnvFile) != 1 {
		t.Errorf("replicas %d env_file %v", svc.Deploy.Replicas, svc.EnvFile)
	}
	if !strings.HasPrefix(c, "# Written by Bohux.") || !strings.Contains(c, "docker compose -p "+project(ref)) {
		t.Error("compose.yaml lacks the header that says how to run it without Bohux")
	}
}

func TestRenderRejects(t *testing.T) {
	newline := spec()
	newline.Secrets = values{"KEY": "line1\nline2"}
	noImage := spec()
	noImage.Image = ""
	badCPU := spec()
	badCPU.Runtime.Cpu = "lots"
	for name, s := range map[string]provider.Spec{"newline secret": newline, "no image": noImage, "bad cpu": badCPU} {
		_, _, _, err := render(s)
		if err == nil {
			t.Errorf("%s: rendered", name)
		}
		if err != nil && strings.Contains(err.Error(), "line1") {
			t.Errorf("%s: error contains the secret value", name)
		}
	}
}

func TestQuantities(t *testing.T) {
	cpu := map[string]string{"500m": "0.5", "1500m": "1.5", "2": "2", "0.25": "0.25"}
	for in, want := range cpu {
		if got, err := cpuLimit(in); err != nil || got != want {
			t.Errorf("cpu %q = %q, %v; want %q", in, got, err, want)
		}
	}
	mem := map[string]string{"512Mi": "512m", "1Gi": "1g", "64Ki": "64k", "1000000": "1000000b"}
	for in, want := range mem {
		if got, err := memoryLimit(in); err != nil || got != want {
			t.Errorf("memory %q = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"-1", "x", "0m", "1.5Gi", "10MB"} {
		if _, err := memoryLimit(bad); err == nil {
			t.Errorf("memory %q accepted", bad)
		}
	}
}

func TestProjectName(t *testing.T) {
	got := project(ref)
	if !strings.HasPrefix(got, "bohux-api-prod-") || len(got) != len("bohux-api-prod-")+8 {
		t.Errorf("project = %q", got)
	}
	other := ref
	other.App = "other-id"
	if project(other) == got {
		t.Error("different apps share a project name")
	}
	if p := project(provider.ResourceRef{Environment: "e", App: "a", AppSlug: "../Evil App!"}); strings.ContainsAny(p, "./ !") {
		t.Errorf("unsafe characters in %q", p)
	}
}

func TestParseLogLine(t *testing.T) {
	l, ok := parseLogLine("bohux-api-prod-1234abcd-app-1  | 2026-10-21T10:00:00.123456789Z started r1")
	if !ok || l.Instance != "bohux-api-prod-1234abcd-app-1" || l.Message != "started r1" || l.Time.IsZero() {
		t.Errorf("parsed %+v", l)
	}
	if _, ok := parseLogLine("no separator"); ok {
		t.Error("parsed a line without a prefix")
	}
}

func TestSetReplicas(t *testing.T) {
	composeYAML, _, _, err := render(spec())
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(composeYAML, &doc); err != nil {
		t.Fatal(err)
	}
	if !setReplicas(&doc, 5) {
		t.Fatal("replicas not found")
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatal(err)
	}
	var back composeDoc
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if back.Services[service].Deploy.Replicas != 5 {
		t.Errorf("replicas %d", back.Services[service].Deploy.Replicas)
	}
	if !strings.HasPrefix(string(out), "# Written by Bohux.") {
		t.Error("rewrite dropped the header comment")
	}
}

func TestRenderOptions(t *testing.T) {
	s := spec()
	s.Raw = []byte(`{"networks": ["bohux-infra"], "volumes": [{"source": "bohux-controlplane-data", "target": "/var/lib/bohux"}],
		"publish_address": "127.0.0.1"}`)
	composeYAML, _, _, err := render(s)
	if err != nil {
		t.Fatal(err)
	}
	var doc composeDoc
	if err := yaml.Unmarshal(composeYAML, &doc); err != nil {
		t.Fatal(err)
	}
	svc := doc.Services[service]
	if fmt.Sprint(svc.Networks) != "[default bohux-infra]" || !doc.Networks["bohux-infra"].External {
		t.Errorf("networks = %v / %v", svc.Networks, doc.Networks)
	}
	if strings.Join(svc.Volumes, ",") != "bohux-controlplane-data:/var/lib/bohux" || !doc.Volumes["bohux-controlplane-data"].External {
		t.Errorf("volumes = %v / %v", svc.Volumes, doc.Volumes)
	}
	if strings.Join(svc.Ports, ",") != "127.0.0.1:80:8080/tcp" {
		t.Errorf("ports = %v, want bound to 127.0.0.1", svc.Ports)
	}

	plain, _, _, err := render(spec())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "networks:") || strings.Contains(string(plain), "volumes:") {
		t.Errorf("no options still rendered networks or volumes:\n%s", plain)
	}
}

func TestRenderRejectsOptions(t *testing.T) {
	for _, raw := range []string{
		`{"privileged": true}`,
		`{"volumes": [{"source": "/etc", "target": "/host-etc"}]}`,
		`{"volumes": [{"source": "data", "target": "relative"}]}`,
		`{"volumes": [{"source": "data", "target": "/"}]}`,
		`{"networks": ["host;rm"]}`,
		`{"networks": ["default"]}`,
		`{"publish_address": "localhost"}`,
		`[1]`,
	} {
		s := spec()
		s.Raw = []byte(raw)
		if _, _, _, err := render(s); err == nil {
			t.Errorf("render accepted options %s", raw)
		}
	}
}

func TestRenderIngress(t *testing.T) {
	s := spec()
	s.Ingress = true
	s.Raw = []byte(`{"networks": ["bohux-infra"]}`)
	composeYAML, _, stateJSON, err := render(s)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Services map[string]struct {
			Networks map[string]composeAttach `yaml:"networks"`
		} `yaml:"services"`
		Networks map[string]composeExternal `yaml:"networks"`
	}
	if err := yaml.Unmarshal(composeYAML, &doc); err != nil {
		t.Fatal(err)
	}
	nets := doc.Services[service].Networks
	if _, ok := nets["default"]; !ok || len(nets) != 3 {
		t.Errorf("networks = %v, want default, bohux-infra and the ingress", nets)
	}
	if a := nets[IngressNetwork].Aliases; len(a) != 1 || a[0] != project(s.Ref) {
		t.Errorf("ingress aliases = %v, want the project name", a)
	}
	if !doc.Networks[IngressNetwork].External || !doc.Networks["bohux-infra"].External {
		t.Errorf("top-level networks = %v", doc.Networks)
	}
	if !strings.Contains(string(stateJSON), `"ingress_ports":[8080,9090]`) {
		t.Errorf("state = %s", stateJSON)
	}
}

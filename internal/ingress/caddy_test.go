package ingress

import (
	"encoding/json"
	"testing"
)

func TestRender(t *testing.T) {
	c := &Caddy{AdminSocket: "/run/bohux-caddy/admin.sock", ACMEEmail: "ops@example.com"}
	sites := []Site{
		{Host: "www.example.com", Upstreams: []string{"web:8080"}},
		{Host: "api.localhost", Upstreams: []string{"api:9090"}},
	}
	a, err := c.Render(sites)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Render([]Site{sites[1], sites[0]})
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Error("the same sites in another order render differently: every pass would reload")
	}
	var cfg struct {
		Admin struct {
			Listen string `json:"listen"`
		} `json:"admin"`
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []struct {
						Match []struct {
							Host []string `json:"host"`
						} `json:"match"`
					} `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
			TLS struct {
				Automation struct {
					Policies []struct {
						Subjects []string         `json:"subjects"`
						Issuers  []map[string]any `json:"issuers"`
					} `json:"policies"`
				} `json:"automation"`
			} `json:"tls"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(a, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Admin.Listen != "unix//run/bohux-caddy/admin.sock" {
		t.Errorf("admin listen = %q: loading must keep the socket", cfg.Admin.Listen)
	}
	routes := cfg.Apps.HTTP.Servers["bohux"].Routes
	if len(routes) != 2 || routes[0].Match[0].Host[0] != "api.localhost" {
		t.Errorf("routes = %+v", routes)
	}
	p := cfg.Apps.TLS.Automation.Policies
	if len(p) != 2 || p[0].Subjects[0] != "api.localhost" || p[0].Issuers[0]["module"] != "internal" ||
		p[1].Issuers[0]["module"] != "acme" || p[1].Issuers[0]["email"] != "ops@example.com" {
		t.Errorf("tls policies = %+v", p)
	}
	if _, err := c.Render([]Site{{Host: "x.example.com"}}); err == nil {
		t.Error("a site without an upstream rendered")
	}
}

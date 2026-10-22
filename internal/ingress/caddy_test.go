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


// TestRenderWeights: a canary's site renders as weighted round robin over its
// upstreams (ADR-0049), and weights that do not match the upstreams are refused.
func TestRenderWeights(t *testing.T) {
	c := &Caddy{AdminSocket: "/run/bohux-caddy/admin.sock"}
	b, err := c.Render([]Site{{Host: "api.example.com", Upstreams: []string{"api-blue:8080", "api-green:8080"},
		Weights: []int{90, 10}}})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []struct {
						Handle []struct {
							Upstreams []struct {
								Dial string `json:"dial"`
							} `json:"upstreams"`
							LoadBalancing struct {
								SelectionPolicy struct {
									Policy  string `json:"policy"`
									Weights []int  `json:"weights"`
								} `json:"selection_policy"`
							} `json:"load_balancing"`
						} `json:"handle"`
					} `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	h := cfg.Apps.HTTP.Servers["bohux"].Routes[0].Handle[0]
	if len(h.Upstreams) != 2 || h.LoadBalancing.SelectionPolicy.Policy != "weighted_round_robin" ||
		len(h.LoadBalancing.SelectionPolicy.Weights) != 2 || h.LoadBalancing.SelectionPolicy.Weights[1] != 10 {
		t.Errorf("handler = %+v", h)
	}
	if _, err := c.Render([]Site{{Host: "x.example.com", Upstreams: []string{"a:1"}, Weights: []int{1, 2}}}); err == nil {
		t.Error("weights that do not match the upstreams rendered")
	}
}

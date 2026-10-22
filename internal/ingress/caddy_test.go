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

// TestRenderAccessLog: with an access log, every route names its upstream in the log
// and the log keeps no request or response headers (ADR-0050); without one, neither.
func TestRenderAccessLog(t *testing.T) {
	type rendered struct {
		Logging struct {
			Logs map[string]struct {
				Include []string `json:"include"`
				Exclude []string `json:"exclude"`
				Writer  struct {
					Output   string `json:"output"`
					Filename string `json:"filename"`
					RollKeep int    `json:"roll_keep"`
				} `json:"writer"`
				Encoder struct {
					Format string                       `json:"format"`
					Fields map[string]map[string]string `json:"fields"`
				} `json:"encoder"`
			} `json:"logs"`
		} `json:"logging"`
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Logs *struct {
						DefaultLoggerName string `json:"default_logger_name"`
					} `json:"logs"`
					Routes []struct {
						Handle []map[string]any `json:"handle"`
					} `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	sites := []Site{{Host: "api.example.com", Upstreams: []string{"api-blue:8080"}}}
	for _, tt := range []struct {
		name      string
		accessLog string
	}{{"with an access log", "/run/bohux-caddy/access.log"}, {"without", ""}} {
		t.Run(tt.name, func(t *testing.T) {
			b, err := (&Caddy{AdminSocket: "/run/bohux-caddy/admin.sock", AccessLog: tt.accessLog}).Render(sites)
			if err != nil {
				t.Fatal(err)
			}
			var cfg rendered
			if err := json.Unmarshal(b, &cfg); err != nil {
				t.Fatal(err)
			}
			srv := cfg.Apps.HTTP.Servers["bohux"]
			first := srv.Routes[0].Handle[0]
			if tt.accessLog == "" {
				if srv.Logs != nil || cfg.Logging.Logs != nil || first["handler"] != "reverse_proxy" {
					t.Errorf("logging rendered without an access log: %s", b)
				}
				return
			}
			if first["handler"] != "log_append" || first["key"] != "upstream" ||
				first["value"] != "{http.reverse_proxy.upstream.hostport}" ||
				srv.Routes[0].Handle[1]["handler"] != "reverse_proxy" {
				t.Errorf("route handlers = %+v", srv.Routes[0].Handle)
			}
			if srv.Logs == nil || srv.Logs.DefaultLoggerName != "bohux" {
				t.Errorf("server logs = %+v", srv.Logs)
			}
			l := cfg.Logging.Logs["bohux"]
			if l.Writer.Output != "file" || l.Writer.Filename != tt.accessLog || l.Writer.RollKeep != 1 ||
				len(l.Include) != 1 || cfg.Logging.Logs["default"].Exclude[0] != l.Include[0] {
				t.Errorf("access logger = %+v", cfg.Logging.Logs)
			}
			if l.Encoder.Format != "filter" {
				t.Errorf("encoder = %+v", l.Encoder)
			}
			for _, f := range []string{"request", "resp_headers", "user_id"} {
				if l.Encoder.Fields[f]["filter"] != "delete" {
					t.Errorf("field %s is kept: %+v", f, l.Encoder.Fields)
				}
			}
		})
	}
}

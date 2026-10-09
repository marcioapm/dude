package fakelux

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A name real lux refuses (lux internal/spec/spec.go volumeRe) is refused
// here too, with lux's message, so a spec cannot pass only the fake.
func TestASubmitWithANameLuxWouldRefuseIsRefused(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	client := lux.New(srv.URL, "k")
	base := func() lux.Spec {
		return lux.Spec{Image: lux.Image{Ref: "agent:1"}, Workload: lux.Workload{Adapter: "generic", Command: []string{"true"}},
			Volumes: []lux.Volume{{Name: "workspace", Path: "/workspace", Kind: "state"}}}
	}
	repo := func(names ...string) func(*lux.Spec) {
		return func(s *lux.Spec) {
			s.Git = &lux.Git{}
			for _, n := range names {
				s.Git.Repositories = append(s.Git.Repositories, lux.Repository{Name: n, URL: "https://x/" + n})
			}
		}
	}
	egress := func(rules ...lux.EgressRule) func(*lux.Spec) {
		return func(s *lux.Spec) { s.Network = &lux.Network{Egress: rules} }
	}
	for _, c := range []struct {
		name string
		edit func(*lux.Spec)
		want string // "" accepted
	}{
		{"a lowercase repository", repo("bl-billing-api"), ""},
		{"32 characters", repo("abcdefghijklmnopqrstuvwxyz012345"), ""},
		{"an uppercase repository", repo("BILL-billing-api"),
			`invalid spec: git.repositories[0]: invalid or duplicate name "BILL-billing-api"`},
		{"33 characters", repo("abcdefghijklmnopqrstuvwxyz0123456"),
			`invalid spec: git.repositories[0]: invalid or duplicate name "abcdefghijklmnopqrstuvwxyz0123456"`},
		{"a dot", repo("web.app"), `invalid spec: git.repositories[0]: invalid or duplicate name "web.app"`},
		{"a duplicate", repo("web", "web"), `invalid spec: git.repositories[1]: invalid or duplicate name "web"`},
		{"a volume", func(s *lux.Spec) { s.Volumes[0].Name = "Work" },
			`invalid spec: volumes[0]: invalid name "Work" (lowercase, digits, - and _)`},
		{"a service", func(s *lux.Spec) { s.Workload.Services = []lux.Service{{Name: "Dude", URL: "http://x"}} },
			`invalid spec: workload.services[0]: invalid name "Dude" (lowercase, digits, - and _)`},
		{"a server", func(s *lux.Spec) { s.Workload.Servers = []lux.ServerInput{{Name: "web-", Port: 3000}} },
			`invalid spec: workload.servers[0]: invalid name "web-" (1-30 of a-z, 0-9 and -, starting with a letter, not ending in -)`},
		// Egress as lux#68 takes it: a wildcard is the whole first label
		// before a domain of two labels or more, and nothing after it.
		{"a wildcard", egress(lux.EgressRule{Host: "*.github.com"}, lux.EgressRule{Host: "pypi.org"}, lux.EgressRule{CIDR: "10.0.0.0/8"}), ""},
		{"a wildcard over a top-level domain", egress(lux.EgressRule{Host: "pypi.org"}, lux.EgressRule{Host: "*.com"}),
			`invalid spec: network.egress[1]: a wildcard is "*." then a domain of at least two labels, e.g. *.example.com`},
		{"a wildcard inside", egress(lux.EgressRule{Host: "a.*.example.com"}),
			`invalid spec: network.egress[0]: a wildcard is "*." then a domain of at least two labels, e.g. *.example.com`},
		{"a wildcard with a port", egress(lux.EgressRule{Host: "*.example.com:443"}),
			`invalid spec: network.egress[0]: a wildcard is a domain only, without a port or path`},
		{"a rule with both", egress(lux.EgressRule{Host: "a.com", CIDR: "10.0.0.0/8"}),
			`invalid spec: network.egress[0]: exactly one of host or cidr`},
	} {
		spec := base()
		c.edit(&spec)
		_, err := client.Submit(context.Background(), spec, "key-"+c.name)
		le, refused := lux.AsError(err)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.want != "" && (!refused || le.Status != http.StatusUnprocessableEntity || le.Code != "invalid_spec" || le.Message != c.want):
			t.Errorf("%s: err = %v, want 422 invalid_spec %q", c.name, err, c.want)
		}
	}
}

// A resume that adds a repository lux would refuse by name is refused, as
// lux's AddRepositories normalizes the spec again.
func TestAResumeAddingANameLuxWouldRefuseIsRefused(t *testing.T) {
	fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	client := lux.New(srv.URL, "k")
	run, err := client.Submit(context.Background(), lux.Spec{Image: lux.Image{Ref: "agent:1"},
		Workload: lux.Workload{Adapter: "generic", Command: []string{"true"}},
		Git:      &lux.Git{Repositories: []lux.Repository{{Name: "web", URL: "https://x/web"}}}}, "resume-names")
	if err != nil {
		t.Fatal(err)
	}
	awaitRun(t, fake, run.ID, "never running", func(r *Run) bool { return r.State == "running" })
	if err := client.Stop(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	awaitRun(t, fake, run.ID, "never stopped", func(r *Run) bool { return r.State == "stopped" })
	for name, want := range map[string]string{
		"WC-web": `invalid spec: git.repositories[1]: invalid or duplicate name "WC-web"`,
		"web":    `invalid spec: git.repositories[1]: invalid or duplicate name "web"`,
	} {
		_, err := client.Resume(context.Background(), run.ID, lux.ResumeInput{RequestID: "add-" + name,
			AddRepositories: []lux.Repository{{Name: name, URL: "https://x/" + name}}})
		if le, ok := lux.AsError(err); !ok || le.Code != "invalid_spec" || le.Message != want {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
	}
}

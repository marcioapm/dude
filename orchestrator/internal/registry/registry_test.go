package registry

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"
)

const ecrHost = "123456789012.dkr.ecr.eu-west-1.amazonaws.com"

// fakeECR mints a new token on every call, valid for ttl from now().
type fakeECR struct {
	now   func() time.Time
	ttl   time.Duration
	calls int
	err   error
	// attempts counts every call, failed ones included.
	attempts int
}

func (f *fakeECR) GetAuthorizationToken(context.Context, *ecr.GetAuthorizationTokenInput, ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error) {
	f.attempts++
	if f.err != nil {
		return nil, f.err
	}
	f.calls++
	token := base64.StdEncoding.EncodeToString([]byte("AWS:pw-" + string(rune('0'+f.calls))))
	return &ecr.GetAuthorizationTokenOutput{AuthorizationData: []types.AuthorizationData{{
		AuthorizationToken: aws.String(token), ExpiresAt: aws.Time(f.now().Add(f.ttl)),
		ProxyEndpoint: aws.String("https://" + ecrHost),
	}}}, nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestAnECRTokenIsCachedUntilAnHourBeforeItExpires(t *testing.T) {
	c := &clock{time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC)}
	api := &fakeECR{now: c.now, ttl: 12 * time.Hour}
	p := NewECR(ecrHost, api, c.now)
	ctx := context.Background()

	first, err := p.Credential(ctx)
	if err != nil || first != "AWS:pw-1" {
		t.Fatalf("first = %q, %v; want the decoded AWS:<password>", first, err)
	}
	c.t = c.t.Add(10*time.Hour + 59*time.Minute)
	if again, _ := p.Credential(ctx); again != first || api.calls != 1 {
		t.Fatalf("at 10h59m: %q after %d calls, want the cached token", again, api.calls)
	}
	// 11h: an hour before expiry, past the refresh point.
	c.t = c.t.Add(time.Minute)
	if fresh, _ := p.Credential(ctx); fresh != "AWS:pw-2" || api.calls != 2 {
		t.Fatalf("at 11h: %q after %d calls, want a new token", fresh, api.calls)
	}
}

// A freshly minted token valid for RefreshBefore or less (clock skew, a
// stale response) is a failed mint, retried after the back-off, and is
// neither returned nor cached; one valid for longer is.
func TestAFreshTokenTooCloseToExpiryIsAFailedMint(t *testing.T) {
	for _, ttl := range []time.Duration{-time.Minute, 0, 30 * time.Minute, RefreshBefore} {
		c := &clock{time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC)}
		api := &fakeECR{now: c.now, ttl: ttl}
		logs, _ := logTo()
		p := NewECR(ecrHost, api, c.now, logs)
		if v, err := p.Credential(context.Background()); err == nil || v != "" || Permanent(err) {
			t.Fatalf("ttl %v: got %q, %v; want a passing error", ttl, v, err)
		}
		api.ttl = RefreshBefore + time.Second
		c.t = c.t.Add(FirstRetry)
		if v, err := p.Credential(context.Background()); err != nil || v != "AWS:pw-2" {
			t.Fatalf("ttl %v, then a good token: %q, %v; want the second token", ttl, v, err)
		}
	}
}

func TestAnECRFailureIsAnErrorAndNotAStaleToken(t *testing.T) {
	c := &clock{time.Now()}
	api := &fakeECR{now: c.now, ttl: 12 * time.Hour}
	p := NewECR(ecrHost, api, c.now)
	if _, err := p.Credential(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(11 * time.Hour)
	api.err = errors.New("no credentials")
	if v, err := p.Credential(context.Background()); err == nil || v != "" {
		t.Fatalf("got %q, %v; want an error", v, err)
	}
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestTheProviderIsChosenByDUDE_REGISTRY_AUTH(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"", "none"} {
		p, err := FromEnv(ctx, env(map[string]string{"DUDE_REGISTRY_AUTH": mode}), "ghcr.io/acme/agent:1")
		if p != nil || err != nil {
			t.Errorf("%q: %v, %v; want no provider", mode, p, err)
		}
	}
	p, err := FromEnv(ctx, env(map[string]string{"DUDE_REGISTRY_AUTH": "static",
		"DUDE_REGISTRY": "ghcr.io", "DUDE_REGISTRY_CREDENTIAL": "bot:ghp_x"}), "ghcr.io/acme/agent:1")
	if err != nil || p.Registry() != "ghcr.io" {
		t.Fatalf("static: %v, %v", p, err)
	}
	if v, _ := p.Credential(ctx); v != "bot:ghp_x" {
		t.Errorf("static credential = %q", v)
	}
	// No AWS call is made to build it: credentials resolve on first use.
	p, err = FromEnv(ctx, env(map[string]string{"DUDE_REGISTRY_AUTH": "ecr"}), ecrHost+"/dude/agent:1")
	if err != nil || p.Registry() != ecrHost {
		t.Fatalf("ecr: %v, %v", p, err)
	}
}

func TestECRWithAnImageElsewhereRefusesToStart(t *testing.T) {
	for _, image := range []string{"ghcr.io/acme/agent:1", "localhost/dude-runtime:dev", "agent:1",
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com.evil.example/agent:1"} {
		_, err := FromEnv(context.Background(), env(map[string]string{"DUDE_REGISTRY_AUTH": "ecr"}), image)
		if err == nil || !strings.Contains(err.Error(), "DUDE_AGENT_IMAGE") {
			t.Errorf("%s: err = %v, want one naming DUDE_AGENT_IMAGE", image, err)
		}
	}
}

func TestBadRegistrySettingsRefuseToStart(t *testing.T) {
	for name, e := range map[string]map[string]string{
		"unknown mode":           {"DUDE_REGISTRY_AUTH": "gcr"},
		"static, no registry":    {"DUDE_REGISTRY_AUTH": "static", "DUDE_REGISTRY_CREDENTIAL": "u:p"},
		"static, a URL":          {"DUDE_REGISTRY_AUTH": "static", "DUDE_REGISTRY": "https://ghcr.io", "DUDE_REGISTRY_CREDENTIAL": "u:p"},
		"static, no credential":  {"DUDE_REGISTRY_AUTH": "static", "DUDE_REGISTRY": "ghcr.io"},
		"static, localhost":      {"DUDE_REGISTRY_AUTH": "static", "DUDE_REGISTRY": "localhost:5000", "DUDE_REGISTRY_CREDENTIAL": "u:p"},
		"registry without mode":  {"DUDE_REGISTRY": "ghcr.io", "DUDE_REGISTRY_CREDENTIAL": "u:p"},
		"ecr with a static host": {"DUDE_REGISTRY_AUTH": "ecr", "DUDE_REGISTRY": "ghcr.io"},
	} {
		if _, err := FromEnv(context.Background(), env(e), ecrHost+"/agent:1"); err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "u:p") {
			t.Errorf("%s: the error carries the credential: %v", name, err)
		}
	}
}

// A static registry lux's validRegistry (lux internal/spec/spec.go) would
// refuse stops the orchestrator at startup; one it takes starts it.
func TestAStaticRegistryIsCheckedAsLuxChecksIt(t *testing.T) {
	for registry, ok := range map[string]bool{
		"ghcr.io":                 true,
		"registry.example:5000":   true,
		"registry.example:1":      true,
		"registry.example:65535":  true,
		"10.0.0.5":                true,
		"10.0.0.5:5000":           true,
		"registry.example:0":      false,
		"registry.example:65536":  false,
		"registry.example:99999":  false,
		"registry.example:05000":  false,
		"registry.example:":       false,
		"registry.example:+50":    false,
		"127.0.0.1":               false,
		"127.0.0.1:5000":          false,
		"127.1.2.3":               false,
		"0.0.0.0":                 false,
		"0.0.0.0:5000":            false,
		"169.254.169.254":         false,
		"169.254.1.1:5000":        false,
		"localhost":               false,
		"registry.localhost:5000": false,
		"GHCR.io":                 false,
		"ghcr.io/acme":            false,
	} {
		_, err := FromEnv(context.Background(), env(map[string]string{"DUDE_REGISTRY_AUTH": "static",
			"DUDE_REGISTRY": registry, "DUDE_REGISTRY_CREDENTIAL": "u:p"}), "agent:1")
		if ok && err != nil {
			t.Errorf("%q: refused: %v", registry, err)
		}
		if !ok && (err == nil || !strings.Contains(err.Error(), "DUDE_REGISTRY")) {
			t.Errorf("%q: err = %v, want the orchestrator refused, naming DUDE_REGISTRY", registry, err)
		}
	}
}

func TestImageRegistryIsWherePodmanPullsFrom(t *testing.T) {
	for ref, want := range map[string]string{
		ecrHost + "/dude/agent:1":        ecrHost,
		"GHCR.io/acme/agent@sha256:abc":  "ghcr.io",
		"10.0.0.5:5000/agent:1":          "10.0.0.5:5000",
		"localhost/dude-runtime:dev":     "localhost",
		"acme/agent:1":                   "docker.io",
		"agent:1":                        "docker.io",
		"registry.example/team/agent:v2": "registry.example",
	} {
		if got := ImageRegistry(ref); got != want {
			t.Errorf("ImageRegistry(%q) = %q, want %q", ref, got, want)
		}
	}
}

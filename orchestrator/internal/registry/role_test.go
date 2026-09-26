package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
)

const role = "arn:aws:iam::123456789012:role/dude-ecr-pull"

func TestTheRoleIsTakenOnlyWithECR(t *testing.T) {
	ctx := context.Background()
	for _, arn := range []string{role, "arn:aws:iam::123456789012:role/ci/pull/dude-ecr_pull.v2",
		"arn:aws-cn:iam::123456789012:role/pull", "arn:aws-us-gov:iam::123456789012:role/pull"} {
		p, err := FromEnv(ctx, env(map[string]string{"DUDE_REGISTRY_AUTH": "ecr", "DUDE_ECR_ROLE_ARN": arn}), ecrHost+"/agent:1")
		if err != nil || MintedBy(p) != arn {
			t.Errorf("%s: %v, minted by %q; want the role", arn, err, MintedBy(p))
		}
	}
	if p, err := FromEnv(ctx, env(map[string]string{"DUDE_REGISTRY_AUTH": "ecr"}), ecrHost+"/agent:1"); err != nil || MintedBy(p) != "host credentials" {
		t.Errorf("no role: %v, minted by %q; want host credentials", err, MintedBy(p))
	}

	for name, e := range map[string]map[string]string{
		"without a mode": {},
		"with none":      {"DUDE_REGISTRY_AUTH": "none"},
		"with static":    {"DUDE_REGISTRY_AUTH": "static", "DUDE_REGISTRY": "ghcr.io", "DUDE_REGISTRY_CREDENTIAL": "u:p"},
	} {
		e["DUDE_ECR_ROLE_ARN"] = role
		if _, err := FromEnv(ctx, env(e), ecrHost+"/agent:1"); err == nil || !strings.Contains(err.Error(), "DUDE_ECR_ROLE_ARN") {
			t.Errorf("%s: err = %v, want the orchestrator refused, naming DUDE_ECR_ROLE_ARN", name, err)
		}
	}
	for _, bad := range []string{"dude-ecr-pull", "arn:aws:iam::123456789012:user/dude",
		"arn:aws:iam::12345:role/pull", "arn:aws:iam:eu-west-1:123456789012:role/pull",
		"arn:aws:sts::123456789012:assumed-role/pull/s", "arn:aws:iam::123456789012:role/",
		"arn:aws:iam::123456789012:role/pull ", " " + role, "arn:aws:iam::123456789012:role/pu ll"} {
		_, err := FromEnv(ctx, env(map[string]string{"DUDE_REGISTRY_AUTH": "ecr", "DUDE_ECR_ROLE_ARN": bad}), ecrHost+"/agent:1")
		if err == nil || !strings.Contains(err.Error(), "DUDE_ECR_ROLE_ARN") {
			t.Errorf("%q: err = %v, want the orchestrator refused, naming DUDE_ECR_ROLE_ARN", bad, err)
		}
	}
}

// fakeSTS answers AssumeRole with credentials whose access key numbers the
// call, valid for ttl of real time (the SDK's credential cache reads the
// wall clock).
type fakeSTS struct {
	mu    sync.Mutex
	ttl   time.Duration
	calls []string
	err   error
}

func (f *fakeSTS) AssumeRole(_ context.Context, in *sts.AssumeRoleInput, _ ...func(*sts.Options)) (*sts.AssumeRoleOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.calls = append(f.calls, aws.ToString(in.RoleArn))
	return &sts.AssumeRoleOutput{Credentials: &ststypes.Credentials{
		AccessKeyId:     aws.String("ASIAROLE" + string(rune('0'+len(f.calls)))),
		SecretAccessKey: aws.String("role-secret"), SessionToken: aws.String("role-session"),
		Expiration: aws.Time(time.Now().Add(f.ttl)),
	}}, nil
}

func (f *fakeSTS) assumed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

var signedBy = regexp.MustCompile(`Credential=([A-Z0-9]+)/`)

// ecrServer is ECR's API over HTTP, for a real *ecr.Client: it records the
// access key that signed each GetAuthorizationToken and answers with a
// 12-hour token.
func ecrServer(t *testing.T) (*ecr.Client, func() []string) {
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := signedBy.FindStringSubmatch(r.Header.Get("Authorization"))
		if m == nil || !strings.HasSuffix(r.Header.Get("X-Amz-Target"), ".GetAuthorizationToken") {
			http.Error(w, "unsigned or unexpected", http.StatusBadRequest)
			return
		}
		mu.Lock()
		keys = append(keys, m[1])
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_ = json.NewEncoder(w).Encode(map[string]any{"authorizationData": []map[string]any{{
			"authorizationToken": base64.StdEncoding.EncodeToString([]byte("AWS:pw-" + m[1])),
			"expiresAt":          time.Now().Add(12 * time.Hour).Unix(),
		}}})
	}))
	t.Cleanup(srv.Close)
	client := ecr.New(ecr.Options{Region: "eu-west-1", BaseEndpoint: aws.String(srv.URL),
		Credentials: credentials.NewStaticCredentialsProvider("AKIAHOST", "host-secret", "")})
	return client, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), keys...)
	}
}

func TestTokensAreMintedWithTheAssumedRolesCredentials(t *testing.T) {
	client, signers := ecrServer(t)
	api := &fakeSTS{ttl: time.Hour}
	c := &clock{time.Now()}
	p := NewECRWithRole(ecrHost, role, client, api, c.now)

	v, err := p.Credential(context.Background())
	if err != nil || v != "AWS:pw-ASIAROLE1" {
		t.Fatalf("credential = %q, %v; want the token minted as the role", v, err)
	}
	if got := signers(); len(got) != 1 || got[0] != "ASIAROLE1" {
		t.Fatalf("GetAuthorizationToken signed by %v, want the assumed role's key only", got)
	}
	if got := api.assumed(); len(got) != 1 || got[0] != role {
		t.Fatalf("AssumeRole calls = %v, want one for %s", got, role)
	}
	// The next token, 11 hours on, reuses the role's credentials while
	// they are more than RoleExpiryWindow from expiring.
	c.t = c.t.Add(11 * time.Hour)
	if v, err := p.Credential(context.Background()); err != nil || v != "AWS:pw-ASIAROLE1" {
		t.Fatalf("second = %q, %v", v, err)
	}
	if len(signers()) != 2 || len(api.assumed()) != 1 {
		t.Fatalf("signers %v, AssumeRole %d times; want the cached role credentials reused", signers(), len(api.assumed()))
	}
}

func TestTheRoleIsAssumedAgainBeforeItsCredentialsExpire(t *testing.T) {
	client, signers := ecrServer(t)
	// Inside RoleExpiryWindow from the moment they are issued.
	api := &fakeSTS{ttl: RoleExpiryWindow - time.Minute}
	c := &clock{time.Now()}
	p := NewECRWithRole(ecrHost, role, client, api, c.now)
	if _, err := p.Credential(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(11 * time.Hour)
	if _, err := p.Credential(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := signers(); len(got) != 2 || got[1] != "ASIAROLE2" || len(api.assumed()) != 2 {
		t.Fatalf("signers %v after %d AssumeRoles; want the second token signed by fresh role credentials", got, len(api.assumed()))
	}
}

func TestWithoutARoleTheHostsCredentialsMint(t *testing.T) {
	client, signers := ecrServer(t)
	p := NewECR(ecrHost, client, time.Now)
	if v, err := p.Credential(context.Background()); err != nil || v != "AWS:pw-AKIAHOST" {
		t.Fatalf("credential = %q, %v", v, err)
	}
	if got := signers(); len(got) != 1 || got[0] != "AKIAHOST" {
		t.Fatalf("signed by %v, want the host's key", got)
	}
}

func TestAFailedAssumeRoleIsAnErrorWithoutAnECRCall(t *testing.T) {
	client, signers := ecrServer(t)
	api := &fakeSTS{ttl: time.Hour, err: errors.New("AccessDenied: not authorized to perform sts:AssumeRole")}
	p := NewECRWithRole(ecrHost, role, client, api, time.Now)
	v, err := p.Credential(context.Background())
	if err == nil || v != "" || !strings.Contains(err.Error(), "AssumeRole") {
		t.Fatalf("got %q, %v; want an AssumeRole error", v, err)
	}
	if len(signers()) != 0 {
		t.Fatalf("ECR was called with the host's credentials after AssumeRole failed: %v", signers())
	}
	api.mu.Lock()
	api.err = nil
	api.mu.Unlock()
	if v, err := p.Credential(context.Background()); err != nil || v != "AWS:pw-ASIAROLE1" {
		t.Fatalf("after recovery: %q, %v", v, err)
	}
}

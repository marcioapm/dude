package registry

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
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
	// attempts counts every call, failed ones included.
	attempts int
}

func (f *fakeSTS) AssumeRole(_ context.Context, in *sts.AssumeRoleInput, _ ...func(*sts.Options)) (*sts.AssumeRoleOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.err != nil {
		return nil, f.err
	}
	f.calls = append(f.calls, aws.ToString(in.RoleArn))
	return &sts.AssumeRoleOutput{Credentials: &ststypes.Credentials{
		AccessKeyId:     aws.String("ASIAROLE" + string(rune('0'+len(f.calls)))),
		SecretAccessKey: aws.String(roleSecret), SessionToken: aws.String(roleSession),
		Expiration: aws.Time(time.Now().Add(f.ttl)),
	}}, nil
}

// The identities the fake ECR knows: the host's long-lived key, and any
// key fakeSTS issues, with its session token.
const (
	hostKey, hostSecret     = "AKIAHOST", "host-secret"
	roleSecret, roleSession = "role-secret", "role-session"
)

func (f *fakeSTS) assumed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

var sigv4Auth = regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=([A-Z0-9]+)/[0-9]{8}/([a-z0-9-]+)/([a-z]+)/aws4_request, SignedHeaders=([a-z0-9;-]+), Signature=[0-9a-f]{64}$`)

// verifySigV4 is what ECR checks of a request: the key is one it knows,
// the session token is that key's (none for a long-lived key), and the
// signature is the key's secret over the request as received, body
// included. Returns the access key.
func verifySigV4(r *http.Request, body []byte) (string, error) {
	auth := r.Header.Get("Authorization")
	m := sigv4Auth.FindStringSubmatch(auth)
	if m == nil {
		return "", errors.New("not SigV4")
	}
	key, region, service, signed := m[1], m[2], m[3], strings.Split(m[4], ";")
	var creds aws.Credentials
	switch {
	case key == hostKey:
		creds = aws.Credentials{AccessKeyID: key, SecretAccessKey: hostSecret}
	case strings.HasPrefix(key, "ASIAROLE"):
		creds = aws.Credentials{AccessKeyID: key, SecretAccessKey: roleSecret, SessionToken: roleSession}
	default:
		return "", errors.New("unknown access key")
	}
	if r.Header.Get("X-Amz-Security-Token") != creds.SessionToken {
		return "", errors.New("security token does not match the access key")
	}
	at, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		return "", err
	}
	// Re-signed with only the headers the client signed: its transport adds
	// others (Accept-Encoding) after signing.
	req := r.Clone(r.Context())
	req.URL.Scheme, req.URL.Host = "http", r.Host
	req.Header = http.Header{}
	for _, h := range signed {
		if h != "host" && h != "x-amz-date" && h != "x-amz-security-token" {
			req.Header[http.CanonicalHeaderKey(h)] = r.Header.Values(h)
		}
	}
	sum := sha256.Sum256(body)
	if err := v4.NewSigner().SignHTTP(r.Context(), creds, req, hex.EncodeToString(sum[:]), service, region, at); err != nil {
		return "", err
	}
	if req.Header.Get("Authorization") != auth {
		return "", errors.New("signature does not match")
	}
	return key, nil
}

// ecrServer is ECR's API over HTTP, for a real *ecr.Client signing as the
// host (hostKey): it verifies each GetAuthorizationToken's signature and
// session token (403 otherwise), records the access key that signed it,
// and answers with a token valid 12 hours from now().
func ecrServer(t *testing.T, now func() time.Time) (*ecr.Client, func() []string) {
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.Header.Get("X-Amz-Target"), ".GetAuthorizationToken") {
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		key, err := verifySigV4(r, body)
		if err != nil {
			w.Header().Set("Content-Type", "application/x-amz-json-1.1")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"__type": "UnrecognizedClientException", "message": err.Error()})
			return
		}
		mu.Lock()
		keys = append(keys, key)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_ = json.NewEncoder(w).Encode(map[string]any{"authorizationData": []map[string]any{{
			"authorizationToken": base64.StdEncoding.EncodeToString([]byte("AWS:pw-" + key)),
			"expiresAt":          now().Add(12 * time.Hour).Unix(),
		}}})
	}))
	t.Cleanup(srv.Close)
	client := ecr.New(ecr.Options{Region: "eu-west-1", BaseEndpoint: aws.String(srv.URL),
		Credentials: credentials.NewStaticCredentialsProvider(hostKey, hostSecret, "")})
	return client, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), keys...)
	}
}

// The fake ECR takes only a request signed by the identity it claims: the
// right secret, and the session token of that identity alone.
func TestTheFakeECRRejectsAnotherIdentitysSignature(t *testing.T) {
	client, signers := ecrServer(t, time.Now)
	// Each key ID signs with one secret only: the SDK's signer caches the
	// signing key it derives by access key ID, not by secret.
	for name, c := range map[string]aws.Credentials{
		"a role key signed with the host's secret": {AccessKeyID: "ASIAROLE7", SecretAccessKey: hostSecret, SessionToken: roleSession},
		"a role key without its session token":     {AccessKeyID: "ASIAROLE1", SecretAccessKey: roleSecret},
		"a role key with another session token":    {AccessKeyID: "ASIAROLE1", SecretAccessKey: roleSecret, SessionToken: "other"},
		"the host key with a session token":        {AccessKeyID: hostKey, SecretAccessKey: hostSecret, SessionToken: roleSession},
	} {
		_, err := client.GetAuthorizationToken(context.Background(), &ecr.GetAuthorizationTokenInput{}, func(o *ecr.Options) {
			o.Credentials = credentials.StaticCredentialsProvider{Value: c}
		})
		var re *smithyhttp.ResponseError
		if !errors.As(err, &re) || re.HTTPStatusCode() != http.StatusForbidden {
			t.Errorf("%s: err = %v, want a 403", name, err)
		}
	}
	if got := signers(); len(got) != 0 {
		t.Fatalf("accepted %v", got)
	}
	_, err := client.GetAuthorizationToken(context.Background(), &ecr.GetAuthorizationTokenInput{}, func(o *ecr.Options) {
		o.Credentials = credentials.NewStaticCredentialsProvider("ASIAROLE1", roleSecret, roleSession)
	})
	if _, hostErr := client.GetAuthorizationToken(context.Background(), &ecr.GetAuthorizationTokenInput{}); err != nil || hostErr != nil {
		t.Fatalf("the role's and the host's own credentials: %v, %v", err, hostErr)
	}
}

func TestTokensAreMintedWithTheAssumedRolesCredentials(t *testing.T) {
	c := &clock{time.Now()}
	client, signers := ecrServer(t, c.now)
	api := &fakeSTS{ttl: time.Hour}
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

// The SDK's credential cache reads the wall clock (aws-sdk-go-v2's
// internal/sdk.NowTime, which cannot be set from outside), so the boundary
// is crossed in real time: credentials issued valid for RoleExpiryWindow
// plus margin are outside the window for margin, then inside it.
func TestTheRoleIsAssumedAgainBeforeItsCredentialsExpire(t *testing.T) {
	const margin = 750 * time.Millisecond
	c := &clock{time.Now()}
	client, signers := ecrServer(t, c.now)
	api := &fakeSTS{ttl: RoleExpiryWindow + margin}
	p := NewECRWithRole(ecrHost, role, client, api, c.now)
	issued := time.Now()
	if _, err := p.Credential(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Past the ECR token's refresh point (its own clock), with the role's
	// credentials still outside RoleExpiryWindow: reused.
	c.t = c.t.Add(11 * time.Hour)
	if _, err := p.Credential(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(issued) >= margin {
		t.Skip("too slow to observe the credentials outside the window")
	}
	if got := signers(); len(got) != 2 || got[1] != "ASIAROLE1" || len(api.assumed()) != 1 {
		t.Fatalf("signers %v after %d AssumeRoles; want the role's credentials reused outside the window", got, len(api.assumed()))
	}
	// Across the boundary on the SDK's clock: inside the window, still
	// before the credentials' real expiry.
	time.Sleep(time.Until(issued.Add(margin + 100*time.Millisecond)))
	c.t = c.t.Add(11 * time.Hour)
	if _, err := p.Credential(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := signers(); len(got) != 3 || got[2] != "ASIAROLE2" || len(api.assumed()) != 2 {
		t.Fatalf("signers %v after %d AssumeRoles; want the third token signed by freshly assumed credentials", got, len(api.assumed()))
	}
}

func TestWithoutARoleTheHostsCredentialsMint(t *testing.T) {
	client, signers := ecrServer(t, time.Now)
	p := NewECR(ecrHost, client, time.Now)
	if v, err := p.Credential(context.Background()); err != nil || v != "AWS:pw-AKIAHOST" {
		t.Fatalf("credential = %q, %v", v, err)
	}
	if got := signers(); len(got) != 1 || got[0] != "AKIAHOST" {
		t.Fatalf("signed by %v, want the host's key", got)
	}
}

func TestAFailedAssumeRoleIsAnErrorWithoutAnECRCall(t *testing.T) {
	c := &clock{time.Now()}
	client, signers := ecrServer(t, c.now)
	api := &fakeSTS{ttl: time.Hour, err: errors.New("AccessDenied: not authorized to perform sts:AssumeRole")}
	p := NewECRWithRole(ecrHost, role, client, api, c.now)
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
	c.t = c.t.Add(FirstRetry)
	if v, err := p.Credential(context.Background()); err != nil || v != "AWS:pw-ASIAROLE1" {
		t.Fatalf("after recovery: %q, %v", v, err)
	}
}

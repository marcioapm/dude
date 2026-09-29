// Package registry logs lux's runners in to the registry agent images are
// pulled from.
//
// lux takes a login per Run (image.registryAuth plus a secret holding
// user:password) and keeps no secret: every resume supplies it again. A
// Provider is where dude gets that login, fresh for each start of a Run —
// an ECR token lasts 12 hours, and a Run may be resumed days later.
package registry

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Provider is the login for one registry.
type Provider interface {
	// Registry is the host (with an optional port) the login is for, as
	// lux's registryAuth names it.
	Registry() string
	// Credential is the login's secret value, user:password, valid for at
	// least the next hour. Errors never carry the value.
	Credential(ctx context.Context) (string, error)
}

// FromEnv chooses the provider DUDE_REGISTRY_AUTH names: nil for none,
// static (DUDE_REGISTRY, DUDE_REGISTRY_CREDENTIAL) or ecr, whose registry
// is agentImage's, minting as DUDE_ECR_ROLE_ARN if set. getenv is
// os.Getenv outside tests.
func FromEnv(ctx context.Context, getenv func(string) string, agentImage string) (Provider, error) {
	registry, credential := getenv("DUDE_REGISTRY"), getenv("DUDE_REGISTRY_CREDENTIAL")
	role := getenv("DUDE_ECR_ROLE_ARN")
	mode := getenv("DUDE_REGISTRY_AUTH")
	if role != "" && mode != "ecr" {
		return nil, errors.New("DUDE_ECR_ROLE_ARN needs DUDE_REGISTRY_AUTH=ecr")
	}
	switch mode {
	case "", "none":
		// Set without a mode, they would silently log in to nothing.
		if registry != "" || credential != "" {
			return nil, errors.New("DUDE_REGISTRY and DUDE_REGISTRY_CREDENTIAL need DUDE_REGISTRY_AUTH=static")
		}
		return nil, nil
	case "static":
		return NewStatic(registry, credential)
	case "ecr":
		host := ImageRegistry(agentImage)
		region, ok := ecrRegion(host)
		if !ok {
			return nil, fmt.Errorf("DUDE_REGISTRY_AUTH=ecr needs DUDE_AGENT_IMAGE in an ECR registry "+
				"(<account>.dkr.ecr.<region>.amazonaws.com/...), not %q", agentImage)
		}
		if registry != "" || credential != "" {
			return nil, errors.New("DUDE_REGISTRY_AUTH=ecr takes its registry from DUDE_AGENT_IMAGE: unset DUDE_REGISTRY and DUDE_REGISTRY_CREDENTIAL")
		}
		if role != "" && !roleARNRe.MatchString(role) {
			return nil, fmt.Errorf("DUDE_ECR_ROLE_ARN: %q is not an IAM role ARN (arn:<partition>:iam::<account>:role/<name>)", role)
		}
		cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
		if err != nil {
			return nil, fmt.Errorf("AWS configuration for ECR: %w", err)
		}
		if role != "" {
			return NewECRWithRole(host, role, ecr.NewFromConfig(cfg), sts.NewFromConfig(cfg), time.Now), nil
		}
		return NewECR(host, ecr.NewFromConfig(cfg), time.Now), nil
	default:
		return nil, fmt.Errorf("DUDE_REGISTRY_AUTH: %q is none of none, static, ecr", mode)
	}
}

type static struct{ registry, credential string }

// NewStatic is a fixed login, for GHCR and other registries that take a
// long-lived token.
func NewStatic(registry, credential string) (Provider, error) {
	if !ValidRegistry(registry) {
		return nil, fmt.Errorf("DUDE_REGISTRY: %q is not a registry host lux accepts (lowercase host or IPv4 address, "+
			"optional port 1-65535, no scheme or path, not localhost, loopback, unspecified or link-local)", registry)
	}
	if credential == "" {
		return nil, errors.New("DUDE_REGISTRY_CREDENTIAL is empty: want user:password or a bare token")
	}
	return static{registry, credential}, nil
}

func (s static) Registry() string                           { return s.registry }
func (s static) Credential(context.Context) (string, error) { return s.credential, nil }

// ECRClient is the one ECR call the provider makes; *ecr.Client has it.
type ECRClient interface {
	GetAuthorizationToken(ctx context.Context, in *ecr.GetAuthorizationTokenInput, opts ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error)
}

// RefreshBefore is how long before its expiry a cached ECR token is
// replaced: a Run started with it has that long to pull its image.
const RefreshBefore = time.Hour

type ecrProvider struct {
	registry string
	client   ECRClient
	now      func() time.Time
	// role and creds are set when tokens are minted as an assumed role;
	// otherwise client's own credentials mint them.
	role  string
	creds aws.CredentialsProvider

	mu      sync.Mutex
	value   string
	expires time.Time
}

// NewECR logs in to an ECR registry with GetAuthorizationToken, through
// whatever credentials client has (the default chain: an instance role).
func NewECR(registry string, client ECRClient, now func() time.Time) Provider {
	return &ecrProvider{registry: registry, client: client, now: now}
}

// RoleExpiryWindow is how long before they expire the assumed role's
// credentials are replaced, so a GetAuthorizationToken never signs with
// credentials about to lapse.
const RoleExpiryWindow = 5 * time.Minute

// NewECRWithRole is NewECR minting as role: its credentials come from
// AssumeRole through stsClient, cached until RoleExpiryWindow before they
// expire, and replace client's own on every GetAuthorizationToken. The
// token then carries only the role's permissions, not the host's.
func NewECRWithRole(registry, role string, client ECRClient, stsClient stscreds.AssumeRoleAPIClient, now func() time.Time) Provider {
	creds := aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(stsClient, role, func(o *stscreds.AssumeRoleOptions) {
		o.RoleSessionName = "dude-registry-login"
	}), func(o *aws.CredentialsCacheOptions) { o.ExpiryWindow = RoleExpiryWindow })
	return &ecrProvider{registry: registry, client: client, now: now, role: role, creds: creds}
}

// MintedBy names the identity whose permissions p's tokens carry: the
// assumed role's ARN, "host credentials", or "" for a provider that mints
// nothing (static).
func MintedBy(p Provider) string {
	e, ok := p.(*ecrProvider)
	switch {
	case !ok:
		return ""
	case e.role != "":
		return e.role
	default:
		return "host credentials"
	}
}

func (p *ecrProvider) Registry() string { return p.registry }

// Credential returns the cached token until RefreshBefore its expiry. Held
// under the lock while minting, so concurrent starts ask ECR once.
func (p *ecrProvider) Credential(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.value != "" && p.now().Before(p.expires.Add(-RefreshBefore)) {
		return p.value, nil
	}
	var opts []func(*ecr.Options)
	if p.creds != nil {
		// Assumed first, so a failed AssumeRole is reported as such and ECR
		// is never called with the host's credentials; the call is signed
		// with exactly these.
		assumed, err := p.creds.Retrieve(ctx)
		if err != nil {
			return "", fmt.Errorf("STS AssumeRole %s: %w", p.role, err)
		}
		opts = append(opts, func(o *ecr.Options) {
			o.Credentials = aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return assumed, nil })
		})
	}
	// No registry ids: the token is good for every registry the role may
	// pull from, in any account.
	out, err := p.client.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{}, opts...)
	if err != nil {
		return "", fmt.Errorf("ECR GetAuthorizationToken: %w", err)
	}
	if len(out.AuthorizationData) == 0 || out.AuthorizationData[0].AuthorizationToken == nil || out.AuthorizationData[0].ExpiresAt == nil {
		return "", errors.New("ECR GetAuthorizationToken returned no token")
	}
	data := out.AuthorizationData[0]
	// base64 of AWS:<password>, which is what lux's secret takes.
	decoded, err := base64.StdEncoding.DecodeString(aws.ToString(data.AuthorizationToken))
	if err != nil || !strings.Contains(string(decoded), ":") {
		return "", errors.New("ECR GetAuthorizationToken returned a token that is not base64 of user:password")
	}
	p.value, p.expires = string(decoded), *data.ExpiresAt
	return p.value, nil
}

// ImageRegistry is the registry host of an image reference, lowercased,
// as podman resolves it: the first path component if it looks like a host
// (a dot, a port, or localhost), else docker.io.
func ImageRegistry(ref string) string {
	first, rest, ok := strings.Cut(ref, "/")
	if !ok || !strings.ContainsAny(first, ".:") && first != "localhost" || rest == "" {
		return "docker.io"
	}
	return strings.ToLower(first)
}

var ecrHostRe = regexp.MustCompile(`^[0-9]{12}\.dkr\.ecr\.([a-z0-9-]+)\.amazonaws\.com$`)

// An IAM role ARN: a partition (aws, aws-cn, aws-us-gov…), no region, a
// 12-digit account, and role/ with an optional path; the name and path
// characters are IAM's.
var roleARNRe = regexp.MustCompile(`^arn:aws(-[a-z]+)*:iam::[0-9]{12}:role/([\w+=,.@-]+/)*[\w+=,.@-]{1,64}$`)

func ecrRegion(host string) (string, bool) {
	m := ecrHostRe.FindStringSubmatch(host)
	if m == nil {
		return "", false
	}
	return m[1], true
}

var hostLabelsRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// ValidRegistry is what lux accepts as image.registryAuth[].registry (lux
// internal/spec/spec.go validRegistry), so a bad setting fails at startup
// rather than on every Run lux refuses: a lowercase host name or IPv4
// address, not the runner's own host, and a port 1-65535 in canonical
// decimal.
func ValidRegistry(r string) bool {
	host, port, hasPort := strings.Cut(r, ":")
	if hasPort {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || port != strconv.Itoa(n) {
			return false
		}
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Is4() && !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsLinkLocalUnicast()
	}
	return hostLabelsRe.MatchString(host)
}

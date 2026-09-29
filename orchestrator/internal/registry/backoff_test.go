package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/aws/smithy-go"
)

// logTo is a provider option logging at debug and above into the returned
// buffer.
func logTo() (Option, *bytes.Buffer) {
	var b bytes.Buffer
	return WithLog(slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug}))), &b
}

// Many callers retrying every few seconds through a transient outage make
// one AWS call per back-off step, the steps doubling to MaxRetry; the first
// call after the outage mints for all of them.
func TestFailedMintsBackOffForEveryCaller(t *testing.T) {
	c := &clock{time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC)}
	api := &fakeECR{now: c.now, ttl: 12 * time.Hour, err: errors.New("dial tcp: i/o timeout")}
	logs, buf := logTo()
	// The top of each delay's range, so the steps are exact.
	p := NewECR(ecrHost, api, c.now, logs, WithJitter(func() float64 { return 0.999999999 }))
	ctx := context.Background()

	// 20 Runs asking every 5 seconds for an hour.
	const callers = 20
	start := c.t
	var calledAt []time.Duration
	for c.t.Sub(start) < time.Hour {
		before := api.attempts
		for range callers {
			if v, err := p.Credential(ctx); err == nil || v != "" {
				t.Fatalf("got %q, %v during the outage", v, err)
			}
		}
		if api.attempts > before+1 {
			t.Fatalf("%d AWS calls in one round of %d callers, want at most one", api.attempts-before, callers)
		}
		if api.attempts > before {
			calledAt = append(calledAt, c.t.Sub(start))
		}
		c.t = c.t.Add(5 * time.Second)
	}
	// 0, then 5s, 10s, 20s, 40s, 80s, 160s apart, then 300s: 18 in the hour.
	want := []time.Duration{0}
	for _, gap := range []int{5, 10, 20, 40, 80, 160} {
		want = append(want, want[len(want)-1]+time.Duration(gap)*time.Second)
	}
	for want[len(want)-1]+MaxRetry < time.Hour {
		want = append(want, want[len(want)-1]+MaxRetry)
	}
	if fmt.Sprint(calledAt) != fmt.Sprint(want) {
		t.Fatalf("AWS called at %v\nwant           %v", calledAt, want)
	}
	if n := strings.Count(buf.String(), "level=WARN"); n != 1 {
		t.Errorf("%d warnings for one outage, want 1:\n%s", n, buf)
	}

	api.err = nil
	c.t = start.Add(want[len(want)-1] + MaxRetry)
	for range callers {
		if v, err := p.Credential(ctx); err != nil || v != "AWS:pw-1" {
			t.Fatalf("after the outage: %q, %v", v, err)
		}
	}
	if api.calls != 1 {
		t.Fatalf("%d tokens minted for %d callers after the outage, want 1", api.calls, callers)
	}
	// A new failure after a success starts from FirstRetry again.
	c.t = c.t.Add(11 * time.Hour)
	api.err = errors.New("dial tcp: i/o timeout")
	before := api.attempts
	_, _ = p.Credential(ctx)
	c.t = c.t.Add(FirstRetry)
	_, _ = p.Credential(ctx)
	if api.attempts != before+2 {
		t.Errorf("%d calls a FirstRetry apart after a success, want 2", api.attempts-before)
	}
}

// The delay is drawn from the upper half of each step: never shorter than
// half of it, never longer than it.
func TestTheBackOffIsJittered(t *testing.T) {
	for _, j := range []float64{0, 0.5, 0.999999999} {
		c := &clock{time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC)}
		api := &fakeECR{now: c.now, err: errors.New("503 Service Unavailable")}
		logs, _ := logTo()
		p := NewECR(ecrHost, api, c.now, logs, WithJitter(func() float64 { return j }))
		_, _ = p.Credential(context.Background())
		want := FirstRetry/2 + time.Duration(j*float64(FirstRetry/2))
		c.t = c.t.Add(want - time.Millisecond)
		_, _ = p.Credential(context.Background())
		if api.attempts != 1 {
			t.Errorf("jitter %v: asked again before %v", j, want)
		}
		c.t = c.t.Add(time.Millisecond)
		_, _ = p.Credential(context.Background())
		if api.attempts != 2 {
			t.Errorf("jitter %v: not asked again at %v", j, want)
		}
	}
}

// An authorization or configuration refusal is logged once at error level
// and retried only every PermanentRetry, however many callers ask.
func TestAnAccessDeniedIsReportedOnceAndRetriedRarely(t *testing.T) {
	for name, denial := range map[string]error{
		"ECR AccessDeniedException": &smithy.GenericAPIError{Code: "AccessDeniedException",
			Message: "not authorized to perform: ecr:GetAuthorizationToken"},
		"STS AccessDenied": &smithy.GenericAPIError{Code: "AccessDenied",
			Message: "not authorized to perform: sts:AssumeRole"},
		"STS region disabled":      &ststypes.RegionDisabledException{},
		"ECR invalid parameter":    &ecrtypes.InvalidParameterException{},
		"STS invalid client token": &smithy.GenericAPIError{Code: "InvalidClientTokenId"},
	} {
		t.Run(name, func(t *testing.T) {
			c := &clock{time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC)}
			api := &fakeECR{now: c.now, ttl: 12 * time.Hour, err: fmt.Errorf("operation error: %w", denial)}
			logs, buf := logTo()
			p := NewECR(ecrHost, api, c.now, logs)
			start := c.t
			for c.t.Sub(start) < time.Hour {
				_, err := p.Credential(context.Background())
				if err == nil || !Permanent(err) {
					t.Fatalf("err = %v, want a permanent failure", err)
				}
				c.t = c.t.Add(5 * time.Second)
			}
			if want := int(time.Hour / PermanentRetry); api.attempts != want {
				t.Errorf("%d AWS calls in an hour, want %d", api.attempts, want)
			}
			if n := strings.Count(buf.String(), "level=ERROR"); n != 1 {
				t.Errorf("%d error-level logs, want one:\n%s", n, buf)
			}
			api.err = nil
			c.t = c.t.Add(PermanentRetry)
			if v, err := p.Credential(context.Background()); err != nil || v != "AWS:pw-1" {
				t.Fatalf("after the fix: %q, %v", v, err)
			}
		})
	}
}

// A failed AssumeRole is throttled as a failed GetAuthorizationToken is:
// STS is not called again until the back-off is over.
func TestAFailedAssumeRoleIsThrottled(t *testing.T) {
	c := &clock{time.Now()}
	client, signers := ecrServer(t, c.now)
	api := &fakeSTS{ttl: time.Hour, err: &smithy.GenericAPIError{Code: "AccessDenied", Message: "not authorized to perform: sts:AssumeRole"}}
	logs, buf := logTo()
	p := NewECRWithRole(ecrHost, role, client, api, c.now, logs)
	for range 50 {
		if _, err := p.Credential(context.Background()); err == nil {
			t.Fatal("no error")
		}
		c.t = c.t.Add(5 * time.Second)
	}
	if api.attempts != 1 || len(signers()) != 0 {
		t.Errorf("%d AssumeRole calls, %d ECR calls in 250s of AccessDenied; want 1 and 0", api.attempts, len(signers()))
	}
	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("the denial was not logged at error level:\n%s", buf)
	}
}

// A context cancelled mid-mint is the caller's, not AWS's failure: it
// starts no back-off.
func TestACancelledCallStartsNoBackOff(t *testing.T) {
	c := &clock{time.Now()}
	api := &fakeECR{now: c.now, ttl: 12 * time.Hour, err: context.Canceled}
	logs, _ := logTo()
	p := NewECR(ecrHost, api, c.now, logs)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = p.Credential(ctx)
	api.err = nil
	if v, err := p.Credential(context.Background()); err != nil || v == "" {
		t.Fatalf("after a cancelled call: %q, %v; want a token at once", v, err)
	}
}

// Concurrent callers during an outage share one AWS call.
func TestConcurrentCallersShareOneFailedCall(t *testing.T) {
	c := &clock{time.Now()}
	api := &fakeECR{now: c.now, err: errors.New("503")}
	logs, _ := logTo()
	p := NewECR(ecrHost, api, c.now, logs)
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { _, _ = p.Credential(context.Background()) })
	}
	wg.Wait()
	if api.attempts != 1 {
		t.Errorf("%d AWS calls from 50 concurrent callers, want 1", api.attempts)
	}
}

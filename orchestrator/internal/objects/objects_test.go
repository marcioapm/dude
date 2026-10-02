package objects

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// s3Stub answers GetObject as S3 does: the object's bytes, or S3's XML
// error with the status S3 gives it.
func s3Stub(t *testing.T, objs map[string][]byte, denied string) *S3 {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/photos/")
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/photos/") {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusBadRequest)
			return
		}
		s3Error := func(status int, code, msg string) {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>` + code + `</Code><Message>` + msg + `</Message></Error>`))
		}
		switch b, ok := objs[key]; {
		case key == denied:
			s3Error(http.StatusForbidden, "AccessDenied", "Access Denied")
		case key == "slow":
			s3Error(http.StatusServiceUnavailable, "SlowDown", "Reduce your request rate")
		case !ok:
			s3Error(http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
		default:
			_, _ = w.Write(b)
		}
	}))
	t.Cleanup(srv.Close)
	s, err := New(Config{Bucket: "photos", Endpoint: srv.URL, Region: "us-east-1", AccessKey: "k", SecretKey: "s"})
	if err != nil {
		t.Fatal(err)
	}
	// The stub answers at once: no SDK retries of the 503 to wait through.
	s.client = s3.New(s.client.Options(), func(o *s3.Options) { o.Retryer = aws.NopRetryer{} })
	return s
}

func TestKeysAreSetTogetherOrNotAtAll(t *testing.T) {
	for _, c := range []Config{{Bucket: "b", AccessKey: "k"}, {Bucket: "b", SecretKey: "s"}} {
		if _, err := New(c); err == nil || err.Error() != "s3.access_key and s3.secret_key are set together or not at all" {
			t.Errorf("%+v: %v", c, err)
		}
	}
	for _, c := range []Config{{Bucket: "b", AccessKey: "k", SecretKey: "s"}, {Bucket: "b"}} {
		if s, err := New(c); err != nil || s == nil {
			t.Errorf("%+v: %v %v", c, s, err)
		}
	}
	if s, err := New(Config{}); err != nil || s != nil {
		t.Errorf("no bucket: %v %v, want no store", s, err)
	}
}

func TestGetReadsAnObjectAndSaysWhyItCannot(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 100)...)
	s := s3Stub(t, map[string][]byte{"a.png": png, "denied.png": png}, "denied.png")
	ctx := context.Background()

	if got, err := s.Get(ctx, "a.png", int64(len(png))); err != nil || !bytes.Equal(got, png) {
		t.Fatalf("an object at the limit: %d bytes, %v", len(got), err)
	}
	if _, err := s.Get(ctx, "gone.png", 1<<20); !errors.Is(err, ErrNotFound) {
		t.Errorf("NoSuchKey: %v, want ErrNotFound", err)
	}
	if _, err := s.Get(ctx, "a.png", int64(len(png))-1); !errors.Is(err, ErrTooLarge) {
		t.Errorf("an object over max: %v, want ErrTooLarge", err)
	}
	var refused *RefusedError
	if _, err := s.Get(ctx, "denied.png", 1<<20); !errors.As(err, &refused) || refused.Status != 403 ||
		refused.Error() != "storage refused it (403 AccessDenied: Access Denied)" {
		t.Errorf("AccessDenied: %v, want a refusal", err)
	}
	// A failing store may recover: neither missing nor refused.
	_, err := s.Get(ctx, "slow", 1<<20)
	if err == nil || errors.Is(err, ErrNotFound) || errors.As(err, &refused) {
		t.Errorf("503: %v, want an error worth retrying", err)
	}
}

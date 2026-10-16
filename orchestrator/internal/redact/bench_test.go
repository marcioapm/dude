package redact_test

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"net/url"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/redact"
)

// benchSecrets are n fake values of a typical credential's length.
func benchSecrets(n int) []redact.Secret {
	list := make([]redact.Secret, n)
	for i := range list {
		list[i] = redact.Secret{Name: fmt.Sprintf("SECRET_%d", i), Value: fmt.Sprintf("bench-test-not-a-real-value-%04d/+&x", i)}
	}
	return list
}

// benchText is size bytes of log-like text with every form of a few of
// the values in it.
func benchText(size int, secrets []redact.Secret) string {
	var b strings.Builder
	rng := rand.New(rand.NewPCG(1, 2))
	for b.Len() < size {
		fmt.Fprintf(&b, "2026-10-16T12:00:%02d step %d ok: wrote %d bytes to /workspace/repos/target/file_%d.go\n",
			rng.IntN(60), rng.IntN(1000), rng.IntN(1<<16), rng.IntN(100))
		if rng.IntN(500) == 0 {
			v := secrets[rng.IntN(len(secrets))].Value
			fmt.Fprintf(&b, "env %s b64 %s hex %s q %s\n", v, base64.StdEncoding.EncodeToString([]byte(v)),
				hex.EncodeToString([]byte(v)), url.QueryEscape(v))
		}
	}
	return b.String()[:size]
}

// BenchmarkString redacts a text artifact; sizes up to redact.MaxText.
func BenchmarkString(b *testing.B) {
	for _, n := range []int{1, 10, 50} {
		secrets := benchSecrets(n)
		r := redact.FromList(secrets)
		for _, size := range []int{64 << 10, 4 << 20, redact.MaxText} {
			text := benchText(size, secrets)
			b.Run(fmt.Sprintf("secrets=%d/bytes=%d", n, size), func(b *testing.B) {
				b.SetBytes(int64(size))
				for b.Loop() {
					_ = r.String(text)
				}
			})
		}
	}
}

// BenchmarkContains scans a diff line by line, as the push scan does.
func BenchmarkContains(b *testing.B) {
	for _, n := range []int{1, 10, 50} {
		secrets := benchSecrets(n)
		r := redact.FromList(secrets)
		lines := strings.Split(benchText(4<<20, []redact.Secret{{Name: "OTHER", Value: "unrelated-test-value"}}), "\n")
		b.Run(fmt.Sprintf("secrets=%d/lines=%d", n, len(lines)), func(b *testing.B) {
			b.SetBytes(4 << 20)
			for b.Loop() {
				for _, l := range lines {
					_, _ = r.Contains(l)
				}
			}
		})
	}
}

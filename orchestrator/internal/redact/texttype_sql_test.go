package redact_test

import (
	"context"
	"os"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/dbtest"
	"github.com/marciomartins/dude/orchestrator/internal/redact"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

// TextTypeSQL, which the copy's retention reads artifacts' content types
// with (phases.copyNeeded), takes for text exactly what TextType, which
// serving reads them with (api.artifactContent), does: a type one counts
// as text and the other not is a file served redacted whose Run's copy is
// deleted, or the reverse.
func TestTextTypeSQLIsTextType(t *testing.T) {
	_, owner := dbtest.Open(t)
	var cases []string
	for ct := range textTypeCases {
		cases = append(cases, ct)
	}
	for _, mt := range redact.TextTypes() {
		cases = append(cases, mt, mt+"; charset=utf-8")
	}
	cases = append(cases,
		// Case.
		"TEXT/PLAIN", "Application/JSON; charset=x", "IMAGE/SVG+XML", "Application/Vnd.A+Json",
		// Whitespace, around the type and before its parameters.
		" text/csv ", "text/markdown\t; charset=utf-8", "application/json\t; charset=utf-8", "\tapplication/json", "application/json\n", "application/xml\r\n; q=1",
		"application/yaml\v", "\fapplication/toml", "  application/sql  ; charset=utf-8", "\u00a0application/json",
		"application/json\u0085", "\u3000text/plain\u2028", "\u1680image/svg+xml\u205f; x", " ", "", ";", "; text/plain",
		// Letters strings.ToLower lowers to ASCII and a collation may not.
		"appl\u0130cation/json", "application/vnd.\u212Aey+json", "T\u0130XT/plain", "\u0130mage/svg+xml",
		// The suffixes.
		"application/vnd.a+xml", "image/svg+xml", "application/vnd.x+json", "application/+json", "+xml",
		// Not text.
		"image/png", "application/octet-stream", "application/jsonx", "application/json5", "xapplication/json",
		"application/x-json-stream", "texts/plain", "text", "application/vnd.a+jsonx", "application/vnd.a+xml2",
		"application/vnd.a+json ; charset=utf-8", "multipart/form-data; boundary=text/plain")
	for _, ct := range cases {
		var got bool
		if err := owner.QueryRow(context.Background(), `SELECT `+redact.TextTypeSQL("$1::text"), ct).Scan(&got); err != nil {
			t.Fatalf("%q: %v", ct, err)
		}
		if want := redact.TextType(ct); got != want {
			t.Errorf("TextTypeSQL(%q) = %v, TextType = %v", ct, got, want)
		}
	}
}

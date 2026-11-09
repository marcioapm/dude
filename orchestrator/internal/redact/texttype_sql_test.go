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
// deleted, or the reverse. Whatever the collation: the column's, the
// database's default, "C", or a Turkish one, under which lower() takes I
// to a dotless ı (checked with the input under each).
func TestTextTypeSQLIsTextType(t *testing.T) {
	_, owner := dbtest.Open(t)
	ctx := context.Background()
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
		// I, which a Turkish collation lowers to a dotless ı; ı and the
		// dotted i's own lowercase, which strings.ToLower leaves non-ASCII.
		"APPLICATION/JSON", "APPLICATION/X-SH", "APPLICATION/VND.I+JSON", "TEXT/PLAIN; CHARSET=X",
		"appl\u0131cation/json", "APPL\u0131CATION/JSON", "application/vnd.\u0131+json",
		// The suffixes.
		"application/vnd.a+xml", "image/svg+xml", "application/vnd.x+json", "application/+json", "+xml",
		// Not text.
		"image/png", "application/octet-stream", "application/jsonx", "application/json5", "xapplication/json",
		"application/x-json-stream", "texts/plain", "text", "application/vnd.a+jsonx", "application/vnd.a+xml2",
		"application/vnd.a+json ; charset=utf-8", "multipart/form-data; boundary=text/plain")
	collations := []string{"", "C", "tr-x-icu"}
	var icu bool
	if err := owner.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_collation WHERE collname = 'tr-x-icu')`).Scan(&icu); err != nil {
		t.Fatal(err)
	}
	if !icu {
		// A server built without ICU has no tr-x-icu collation; libc's
		// tr_TR, when installed, lowers I the same way.
		collations = collations[:2]
		if err := owner.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_collation WHERE collname = 'tr_TR.utf8')`).Scan(&icu); err != nil {
			t.Fatal(err)
		}
		if icu {
			collations = append(collations, "tr_TR.utf8")
		}
	}
	for _, coll := range collations {
		t.Run("collate "+coll, func(t *testing.T) {
			in := "$1::text"
			if coll != "" {
				in += ` COLLATE "` + coll + `"`
			}
			for _, ct := range cases {
				var got bool
				if err := owner.QueryRow(ctx, `SELECT `+redact.TextTypeSQL(in), ct).Scan(&got); err != nil {
					t.Fatalf("%q: %v", ct, err)
				}
				if want := redact.TextType(ct); got != want {
					t.Errorf("TextTypeSQL(%q) = %v, TextType = %v", ct, got, want)
				}
			}
		})
	}
	if len(collations) < 3 {
		t.Skip("no Turkish collation on this server (built without ICU, no tr_TR locale): checked under the default and \"C\" only")
	}
}

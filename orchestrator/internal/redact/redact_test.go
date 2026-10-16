package redact_test

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/redact"
)

const pw = "sf-test-not-a-real-password/123+&"

func TestEveryFormOfAValueIsRedactedByName(t *testing.T) {
	r := redact.New(map[string]string{"SNOWFLAKE_PASSWORD": pw})
	b64 := base64.StdEncoding.EncodeToString([]byte(pw))
	forms := map[string]string{
		"raw":            pw,
		"base64":         b64,
		"base64 no pad":  strings.TrimRight(b64, "="),
		"base64 url":     base64.URLEncoding.EncodeToString([]byte(pw)),
		"query escaped":  url.QueryEscape(pw),
		"path escaped":   url.PathEscape(pw),
		"hex":            hex.EncodeToString([]byte(pw)),
		"json, html esc": strings.Trim(mustJSON(pw), `"`),
	}
	for name, form := range forms {
		got := r.String("before " + form + " after")
		if got != "before [REDACTED:SNOWFLAKE_PASSWORD] after" {
			t.Errorf("%s: %q", name, got)
		}
	}
}

func TestShortValuesAreLeftAloneAndAdjacentMatchesMerge(t *testing.T) {
	r := redact.New(map[string]string{"PIN": "12345", "A": "abcdefgh", "B": "ijklmnop"})
	if got := r.String("pin 12345"); got != "pin 12345" {
		t.Errorf("a 5-byte value was redacted: %q", got)
	}
	if got := r.String("x abcdefghijklmnop y"); got != "x [REDACTED:A] y" {
		t.Errorf("adjacent values = %q, want one marker", got)
	}
	if !redact.New(map[string]string{"PIN": "12345"}).Empty() {
		t.Error("a redactor of only short values is not empty")
	}
}

func TestJSONIsRedactedInEveryStringAndStaysJSON(t *testing.T) {
	r := redact.New(map[string]string{"SNOWFLAKE_PASSWORD": pw})
	in := json.RawMessage(`{"command":"echo ` + strings.Trim(mustJSON(pw), `"`) + `","nested":[{"k":"` + hex.EncodeToString([]byte(pw)) + `"}],"n":3}`)
	out := r.JSON(in)
	if strings.Contains(string(out), "not-a-real") || strings.Contains(string(out), hex.EncodeToString([]byte(pw))) {
		t.Fatalf("value left in %s", out)
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("not JSON after redaction: %v: %s", err, out)
	}
	if v["command"] != "echo [REDACTED:SNOWFLAKE_PASSWORD]" || v["n"] != float64(3) {
		t.Errorf("redacted = %s", out)
	}
	untouched := json.RawMessage(`{"a": 1}`)
	if string(r.JSON(untouched)) != string(untouched) {
		t.Error("JSON with no value was rewritten")
	}
}

func TestOneNameMayCarryItsGivenAndRotatedValues(t *testing.T) {
	r := redact.FromList([]redact.Secret{{"SNOWFLAKE_PASSWORD", "old-test-password"}, {"SNOWFLAKE_PASSWORD", "new-test-password"}})
	if got := r.String("old-test-password new-test-password"); got != "[REDACTED:SNOWFLAKE_PASSWORD] [REDACTED:SNOWFLAKE_PASSWORD]" {
		t.Errorf("got %q", got)
	}
}

// A JSON document whose string holds JSON text carrying the value (a tool
// call whose input is a command with JSON in it) has the value escaped
// twice in its encoding: still redacted, and still JSON.
func TestAValueInJSONTextInsideAJSONStringIsRedacted(t *testing.T) {
	const special = `sf-test-not-a-real/+&"<x`
	r := redact.New(map[string]string{"SNOWFLAKE_PASSWORD": special})
	inner, _ := json.Marshal(map[string]string{"content": "PASSWORD=" + special})
	outer, _ := json.Marshal(map[string]any{"rawInput": map[string]string{"cmd": "write " + string(inner)}})
	out := r.JSON(outer)
	var v struct {
		RawInput struct{ Cmd string } `json:"rawInput"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("not JSON after redaction: %v: %s", err, out)
	}
	if strings.Contains(v.RawInput.Cmd, "not-a-real") || !strings.Contains(v.RawInput.Cmd, "[REDACTED:SNOWFLAKE_PASSWORD]") {
		t.Errorf("cmd = %q", v.RawInput.Cmd)
	}
	if name, ok := r.Contains(string(outer)); !ok || name != "SNOWFLAKE_PASSWORD" {
		t.Errorf("Contains on the encoded document = %q %v", name, ok)
	}
}

func TestContainsNamesTheSecretFound(t *testing.T) {
	r := redact.New(map[string]string{"SNOWFLAKE_PASSWORD": pw, "OTHER": "other-test-value"})
	if name, ok := r.Contains("+FOO=" + base64.StdEncoding.EncodeToString([]byte(pw))); !ok || name != "SNOWFLAKE_PASSWORD" {
		t.Errorf("Contains = %q %v", name, ok)
	}
	if _, ok := r.Contains("nothing here"); ok {
		t.Error("found a value in text without one")
	}
	var none *redact.Redactor
	if none.String("x "+pw) != "x "+pw {
		t.Error("a nil redactor changed text")
	}
}

func TestTextTypes(t *testing.T) {
	for ct, want := range map[string]bool{"text/plain; charset=utf-8": true, "application/json": true, "application/vnd.x+json": true,
		"image/png": false, "video/webm": false, "application/octet-stream": false, "image/svg+xml": true} {
		if redact.TextType(ct) != want {
			t.Errorf("TextType(%q) = %v", ct, !want)
		}
	}
}

func mustJSON(v string) string {
	b, _ := json.Marshal(v)
	return string(b)
}

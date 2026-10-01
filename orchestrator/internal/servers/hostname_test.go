package servers

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func TestPreviewLabelIsOneDNSLabel(t *testing.T) {
	for _, c := range []struct{ server, task, project, want string }{
		{"web", "t123", "p9", "web-t123-p9"},
		{"Web", "T_123", "P.9", "web-t-123-p-9"},
		{"api--v2", "--wi--x--", "prj__y", "api-v2-wi-x-prj-y"},
		{"web", "wi_0mg7x2k1a3f9c1e2d4b5a6f7", "prj_0mg7x2k1a3f9c1e2d4b5a6f78", "web-wi-0mg7x2k1a3f9c1e2d4b5a6f7-prj-0mg7x2k1a3f9c1e2d4b5a6f78"},
	} {
		got := PreviewLabel(c.server, c.task, c.project, "")
		if got != c.want || !dnsLabel.MatchString(got) {
			t.Errorf("PreviewLabel(%q, %q, %q) = %q, want %q", c.server, c.task, c.project, got, c.want)
		}
	}
	if h := PreviewHostname("Preview-ABsmartly.dev.", "web", "t123", "p9", ""); h != "web-t123-p9.preview-absmartly.dev" {
		t.Errorf("hostname = %q", h)
	}
}

// 63 is kept as is; 64 and past are cut and hashed, still one label of at
// most 63, and the same inputs always give the same label.
func TestPreviewLabelAtTheLimit(t *testing.T) {
	task, project := strings.Repeat("t", 29), strings.Repeat("p", 29)
	exactly := PreviewLabel("abc", task, project, "") // 3+1+29+1+29 = 63
	if len(exactly) != 63 || exactly != "abc-"+task+"-"+project {
		t.Fatalf("63 characters were changed: %q (%d)", exactly, len(exactly))
	}
	over := PreviewLabel("abcd", task, project, "")
	if len(over) != 63 || !dnsLabel.MatchString(over) || !regexp.MustCompile(`-[0-9a-f]{8}$`).MatchString(over) {
		t.Fatalf("64 characters became %q (%d)", over, len(over))
	}
	if again := PreviewLabel("abcd", task, project, ""); again != over {
		t.Errorf("not deterministic: %q then %q", over, again)
	}
	long := PreviewLabel(strings.Repeat("s", 30), strings.Repeat("x", 200), "p", "")
	if len(long) > 63 || !dnsLabel.MatchString(long) {
		t.Errorf("a long one is %q (%d)", long, len(long))
	}
	// A cut that ends on a '-' does not leave "--".
	cut := PreviewLabel(strings.Repeat("a", 53), "b", strings.Repeat("c", 20), "")
	if strings.Contains(cut, "--") || !dnsLabel.MatchString(cut) {
		t.Errorf("cut on a dash: %q", cut)
	}
}

// Previews whose labels share their first 54 characters, as long ids of
// one project do, never shorten to the same label.
func TestShortenedLabelsStayDistinct(t *testing.T) {
	seen := map[string]string{}
	project := "prj_0mg7x2k1a3f9c1e2d4b5a6f78"
	for _, server := range []string{"admin", "storybook", "web-admin"} {
		for i := range 5000 {
			task := fmt.Sprintf("wi_0mg7x2k1a3f9c1e2d4b5%06d", i)
			l := PreviewLabel(server, task, project, "")
			if len(l) > 63 || !dnsLabel.MatchString(l) {
				t.Fatalf("%q is not a label", l)
			}
			key := server + "/" + task
			if other, dup := seen[l]; dup {
				t.Fatalf("%s and %s share %q", other, key, l)
			}
			seen[l] = key
		}
	}
	// Normalising alone can make two names equal; the hash of the names as
	// given keeps the shortened forms apart.
	a := PreviewLabel("web_admin", strings.Repeat("t", 60), "p", "")
	b := PreviewLabel("web-admin", strings.Repeat("t", 60), "p", "")
	if a == b {
		t.Errorf("web_admin and web-admin share %q", a)
	}
}

// After hostname_taken, a salted label is another, hashed one.
func TestASaltedLabelIsAnother(t *testing.T) {
	plain := PreviewLabel("web", "t1", "p1", "")
	salted := PreviewLabel("web", "t1", "p1", "run_1")
	if plain == salted || !strings.HasPrefix(salted, "web-t1-p1-") || len(salted) != len("web-t1-p1-")+8 {
		t.Errorf("plain %q, salted %q", plain, salted)
	}
	if PreviewLabel("web", "t1", "p1", "run_2") == salted {
		t.Error("two salts gave one label")
	}
}

package servers

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// of is a preview of a task and project whose ids are derived from their
// key and slug, as distinct as theirs.
func of(key, slug string) PreviewOf {
	return PreviewOf{TaskID: "wi_" + key + "_" + slug, TaskKey: key, ProjectID: "prj_" + slug, ProjectSlug: slug}
}

func TestPreviewLabelIsServerTaskKeyAndProjectSlug(t *testing.T) {
	for _, c := range []struct {
		server string
		of     PreviewOf
		want   string
	}{
		{"web", PreviewOf{TaskID: "wi_0muq7fh8y3cf82dc982bd4ec2", TaskKey: "JERV-2", ProjectID: "prj_0mun9jt6n6ddfba79e68f4bcd",
			ProjectSlug: "jervasion"}, "web-jerv-2-jervasion"},
		{"Web", of("T_123", "p.9"), "web-t-123-p-9"},
		{"api--v2", of("--WI--7--", "my--app"), "api-v2-wi-7-my-app"},
	} {
		got := PreviewLabel(c.server, c.of, "")
		if got != c.want || !dnsLabel.MatchString(got) {
			t.Errorf("PreviewLabel(%q, %+v) = %q, want %q", c.server, c.of, got, c.want)
		}
	}
	if h := PreviewHostname("preview-absmartly.dev", "web", of("JERV-2", "jervasion"), ""); h != "web-jerv-2-jervasion.preview-absmartly.dev" {
		t.Errorf("hostname = %q", h)
	}
}

// Two projects of one org may share a key prefix (it is editable); their
// slugs, unique in the org, keep their previews apart.
func TestProjectsSharingAKeyPrefixGetDifferentLabels(t *testing.T) {
	a := PreviewLabel("web", of("JERV-2", "jerv"), "")
	b := PreviewLabel("web", of("JERV-2", "jervasion"), "")
	if a == b || a != "web-jerv-2-jerv" || b != "web-jerv-2-jervasion" {
		t.Errorf("jerv %q, jervasion %q", a, b)
	}
}

// 63 is kept as is; 64 and past are cut and hashed, still one label of at
// most 63, and the same inputs always give the same label.
func TestPreviewLabelAtTheLimit(t *testing.T) {
	key, slug := strings.Repeat("t", 29), strings.Repeat("p", 29)
	exactly := PreviewLabel("abc", of(key, slug), "") // 3+1+29+1+29 = 63
	if len(exactly) != 63 || exactly != "abc-"+key+"-"+slug {
		t.Fatalf("63 characters were changed: %q (%d)", exactly, len(exactly))
	}
	over := PreviewLabel("abcd", of(key, slug), "")
	if len(over) != 63 || !dnsLabel.MatchString(over) || !regexp.MustCompile(`-[0-9a-f]{8}$`).MatchString(over) {
		t.Fatalf("64 characters became %q (%d)", over, len(over))
	}
	if again := PreviewLabel("abcd", of(key, slug), ""); again != over {
		t.Errorf("not deterministic: %q then %q", over, again)
	}
	long := PreviewLabel(strings.Repeat("s", 30), of(strings.Repeat("x", 200), "p"), "")
	if len(long) > 63 || !dnsLabel.MatchString(long) {
		t.Errorf("a long one is %q (%d)", long, len(long))
	}
	// A cut that ends on a '-' does not leave "--".
	cut := PreviewLabel(strings.Repeat("a", 53), of("b", strings.Repeat("c", 20)), "")
	if strings.Contains(cut, "--") || !dnsLabel.MatchString(cut) {
		t.Errorf("cut on a dash: %q", cut)
	}
}

// The hash is of the ids: a shortened label's suffix is the preview's own,
// the same after its project's key prefix is renamed, and two previews with
// one key and slug (projects of two orgs) never share a shortened label.
func TestTheHashIsOfTheIds(t *testing.T) {
	slug := strings.Repeat("s", 60)
	mine := PreviewOf{TaskID: "wi_1", TaskKey: "JERV-2", ProjectID: "prj_1", ProjectSlug: slug}
	renamed := mine
	renamed.TaskKey = "JV-2"
	theirs := mine
	theirs.TaskID, theirs.ProjectID = "wi_2", "prj_2"
	a, b, c := PreviewLabel("web", mine, ""), PreviewLabel("web", renamed, ""), PreviewLabel("web", theirs, "")
	if a[len(a)-8:] != b[len(b)-8:] {
		t.Errorf("renaming the prefix changed the hash: %q, %q", a, b)
	}
	if a == c {
		t.Errorf("two orgs' previews share %q", a)
	}
}

// With a long project slug, labels remain valid and distinct even when
// shortened.
func TestShortenedLabelsStayDistinct(t *testing.T) {
	seen := map[string]string{}
	slug := "a-project-slug-long-enough-to-fill-most-of-a-label"
	for _, server := range []string{"admin", "storybook", "web-admin"} {
		for i := range 5000 {
			o := PreviewOf{TaskID: fmt.Sprintf("wi_%06d", i), TaskKey: fmt.Sprintf("APSL-%d", i), ProjectID: "prj_1", ProjectSlug: slug}
			l := PreviewLabel(server, o, "")
			if len(l) > 63 || !dnsLabel.MatchString(l) {
				t.Fatalf("%q is not a label", l)
			}
			key := server + "/" + o.TaskKey
			if other, dup := seen[l]; dup {
				t.Fatalf("%s and %s share %q", other, key, l)
			}
			seen[l] = key
		}
	}
	// Normalising alone can make two names equal; the hash of the names as
	// given keeps the shortened forms apart.
	a := PreviewLabel("web_admin", of("T-1", strings.Repeat("t", 60)), "")
	b := PreviewLabel("web-admin", of("T-1", strings.Repeat("t", 60)), "")
	if a == b {
		t.Errorf("web_admin and web-admin share %q", a)
	}
}

// After hostname_taken, a salted label is another, hashed one.
func TestASaltedLabelIsAnother(t *testing.T) {
	o := of("JERV-2", "jervasion")
	plain := PreviewLabel("web", o, "")
	salted := PreviewLabel("web", o, "run_1")
	if plain == salted || !strings.HasPrefix(salted, "web-jerv-2-jervasion-") || len(salted) != len("web-jerv-2-jervasion-")+8 {
		t.Errorf("plain %q, salted %q", plain, salted)
	}
	if PreviewLabel("web", o, "run_2") == salted {
		t.Error("two salts gave one label")
	}
}

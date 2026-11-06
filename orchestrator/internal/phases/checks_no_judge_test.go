package phases

import (
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// The checks gate cannot reach a judgement tier: no file of the phases
// package (tests aside) imports the judge package, and the Syncer has no
// field through which one could be handed to it. The judgement tier routes
// chat (internal/chat); checks are the conductor's (askConductor) or, with
// no conductor, a person's (noConductor).
func TestTheChecksGateCannotReachAJudge(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	read := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		read++
		for _, imp := range f.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); strings.HasSuffix(path, "/internal/judge") {
				t.Errorf("%s imports %s", name, path)
			}
		}
	}
	if read < 10 {
		t.Fatalf("read %d files of the package, not the package", read)
	}
	typ := reflect.TypeOf(Syncer{})
	for i := range typ.NumField() {
		f := typ.Field(i)
		if strings.Contains(strings.ToLower(f.Name), "judge") || strings.HasSuffix(f.Type.PkgPath(), "/internal/judge") ||
			f.Type.Kind() == reflect.Pointer && strings.HasSuffix(f.Type.Elem().PkgPath(), "/internal/judge") {
			t.Errorf("Syncer.%s (%s) could hand the checks gate a judge", f.Name, f.Type)
		}
		// A func or an interface could carry one under any name: each is
		// one the Syncer is known to be handed, of its known type.
		if k := f.Type.Kind(); k == reflect.Func || k == reflect.Interface {
			if want, ok := syncerCallables[f.Name]; !ok || want != f.Type.String() {
				t.Errorf("Syncer.%s (%s) is a func or interface not in syncerCallables: could it hand the checks gate a judge? If not, list it", f.Name, f.Type)
			}
		}
	}
}

// syncerCallables are the Syncer's func and interface fields, none of which
// reaches a judgement tier: name → type.
var syncerCallables = map[string]string{
	"Lux":                "lux.Client",
	"Forges":             "delivery.Forges",
	"Registry":           "registry.Provider",
	"Objects":            "objects.Store",
	"Now":                "func() time.Time",
	"followedUp":         "func()",
	"MergeAttemptLookup": "func()",
	"BaseSyncClaim":      "func(context.Context, pgx.Tx, string)",
}

// The same, by behaviour: with the organisation's judgement tier set and
// the model URL the orchestrator was given answering, the gate sends that
// URL nothing and records no model call, for a policy delivery's failure
// and its passes and for a conducted report the conductor decides.
func TestASetJudgementTierIsNeverAskedAboutChecks(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		conducted   bool
		pass        bool
	}{
		{"a policy failure", failingTsc, false, false},
		{"policy passes", passingChecks, false, true},
		{"a conducted report", passingChecks, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var asked atomic.Int64
			proxy := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				asked.Add(1)
				t.Errorf("the model URL was asked %s %s", r.Method, r.URL.Path)
				rw.WriteHeader(http.StatusInternalServerError)
			}))
			defer proxy.Close()
			w := newHarnessWorld(t)
			w.s.Agent.LLMURL, w.s.Agent.LLMKey = proxy.URL+"/v1", "sk-judge"
			mustOwner(t, w, `INSERT INTO model_tiers (id, organization_id, name, model) VALUES ('tier_j_'||$1, $1, 'Judge', 'judge-model')`, w.run.Org)
			mustOwner(t, w, `UPDATE organizations SET judgement_tier_id = 'tier_j_'||$1 WHERE id = $1`, w.run.Org)
			if n := w.count(`SELECT count(*) FROM organizations WHERE id = $1 AND judgement_tier_id IS NOT NULL`, w.run.Org); n != 1 {
				t.Fatal("the judgement tier is not set")
			}
			var cond string
			if tc.conducted {
				cond = conducted(w)
			} else {
				delivering(w, false)
			}
			checksReply(w, tc.reply)
			endTurn(w)
			pass, acted := w.gate()
			if tc.conducted {
				if pass || acted {
					t.Fatalf("before the verdict: pass %v acted %v", pass, acted)
				}
				if _, err := w.decide(cond, backedAll(w.run.ID, w.turn())); err != nil {
					t.Fatal(err)
				}
				pass, acted = w.gate()
			}
			if pass != tc.pass || !acted {
				t.Fatalf("pass %v acted %v, want pass %v", pass, acted, tc.pass)
			}
			if n := w.judgedCount(); n != 1 {
				t.Fatalf("%d checks judgements recorded", n)
			}
			if n := asked.Load(); n != 0 {
				t.Errorf("%d requests to the model URL", n)
			}
			if n := w.count(`SELECT count(*) FROM model_calls WHERE organization_id = $1`, w.run.Org); n != 0 {
				t.Errorf("%d model calls recorded", n)
			}
		})
	}
}

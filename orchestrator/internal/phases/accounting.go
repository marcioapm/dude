package phases

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// accountingTimeout bounds the accounting Exec, as diffTimeout bounds
// readDiff: a hung lux must not hold up the sweep. Sized for long histories.
var accountingTimeout = 60 * time.Second

// accountingScript prints what start..head changed that is the task's own,
// as NUL-separated records. base is a commit dude trusts as the base (the
// git.sync lux reported, checked against GitHub: trustedBase), never a ref
// the checkout names; "" excludes nothing. Commits reachable from base are
// not walked, so a fast-forward onto the base counts nothing. Each run of
// ordinary first-parent commits is one net diff; a two-parent merge whose
// second parent is in base counts only its resolution (--remerge-diff).
//
// The checkout is the agent's, so nothing in it is trusted but its objects:
// the diffs run in a scratch bare repository that borrows them
// (objects/info/alternates), with no global or system config. Its refs
// (refs/replace), config (diff drivers, textconv), info/attributes and a
// committed .gitattributes (attr.tree is the empty tree) never apply; the
// flags say the same again for a git that reads more than this.
const accountingScript = `set -eu
export GIT_NO_REPLACE_OBJECTS=1 GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_ATTR_NOSYSTEM=1
repo=$1 start=$2 head=$3 b=$4
gd=$(git -C "$repo" rev-parse --path-format=absolute --git-common-dir)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
git init -q --bare "$tmp/r"
printf '%s/objects\n' "$gd" > "$tmp/r/objects/info/alternates"
if [ -f "$gd/shallow" ]; then cp "$gd/shallow" "$tmp/r/shallow"; fi
g() { git --git-dir="$tmp/r" -c diff.external= -c core.attributesFile=/dev/null -c attr.tree=4b825dc642cb6eb9a060e54bf8d69288fbee4904 "$@"; }
d() { g "$@" --no-ext-diff --no-textconv --text -M --raw --numstat -z; }
g cat-file -e "$start^{commit}"
g cat-file -e "$head^{commit}"
if [ -n "$b" ]; then g cat-file -e "$b^{commit}"; fi
g merge-base --is-ancestor "$start" "$head"
printf 'DUDE_ACCOUNTING\t%s\t%s\t%s\0' "$start" "$head" "$b"
commits=$(g rev-list --first-parent --reverse --parents "$start..$head" ${b:+--not "$b"})
printf '%s\n' "$commits" | {
seg="" last=""
flush() { if [ -n "$seg" ]; then printf 'DUDE_SEGMENT\0'; d diff "$seg" "$last"; fi; seg=""; }
while read -r c p1 p2 more; do
 [ -n "$c" ] || continue
 if [ -z "$p1" ] || [ -n "$more" ]; then
  printf 'unsupported commit %s: a root or an octopus merge\n' "$c" >&2
  exit 3
 fi
 if [ -n "$p2" ] && [ -n "$b" ] && g merge-base --is-ancestor "$p2" "$b"; then
  flush
  printf 'DUDE_MERGE\0'
  d show --format= --remerge-diff "$c"
  continue
 fi
 if [ -n "$seg" ] && [ "$p1" != "$last" ]; then flush; fi
 if [ -z "$seg" ]; then seg=$p1; fi
 last=$c
done
flush
}
printf 'DUDE_END\t%s\0' "$head"
`

// accounting is what a publish counts as the task's own change.
type accounting struct {
	Paths []string
	Lines int
	// A numstat said "-": a binary change, or one the checkout's
	// attributes keep from being counted. Over any line limit.
	Binary bool
	// The evidence's lines by path, a rename's on its new name.
	ByPath map[string]int
	// The evidence's paths that numstat could not count.
	binary map[string]bool
	// Why the count is GitHub's whole comparison, nothing of the base left
	// out; "" when the trusted base and the evidence were used.
	Full string
}

// errAccountingTransient is an accounting that did not complete for a
// reason that may pass: a timeout, an unreachable or restarting lux, a
// stream that ended early, a container not running now.
type errAccountingTransient struct{ error }

func (e errAccountingTransient) Unwrap() error { return e.error }

// accountingTransient says the accounting may be asked again later.
func accountingTransient(err error) bool {
	return errors.As(err, new(errAccountingTransient))
}

// publicationAccounting reads the task's own change from the Run's
// checkout. base is a commit trustedBase vouched for, "" for none. Anything
// short of complete, readable evidence is an error; one that may pass is an
// errAccountingTransient.
func publicationAccounting(ctx context.Context, l lux.Client, runID, start, head, base, repo string) (accounting, error) {
	if start == "" || head == "" {
		return accounting{}, fmt.Errorf("publication accounting needs exact start and head")
	}
	ctx, cancel := context.WithTimeout(ctx, accountingTimeout)
	defer cancel()
	res, err := l.Exec(ctx, runID, []string{"sh", "-c", accountingScript, "account", "/workspace/repos/" + lux.SpecName(repo), start, head, base})
	if err != nil {
		le, isLux := lux.AsError(err)
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || isLux && (le.Retryable() || le.Code == "not_running") {
			return accounting{}, errAccountingTransient{err}
		}
		return accounting{}, err
	}
	if res.ExitCode != 0 {
		return accounting{}, fmt.Errorf("publication accounting exited %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return parseAccounting(string(res.Stdout), start, head)
}

// parseAccounting reads accountingScript's records. Paths come from the raw
// records only (both sides of a rename or copy); lines from numstat.
func parseAccounting(text, start, head string) (accounting, error) {
	var out accounting
	tokens := strings.Split(text, "\x00")
	if len(tokens) < 3 || tokens[len(tokens)-1] != "" || tokens[len(tokens)-2] != "DUDE_END\t"+head ||
		!strings.HasPrefix(tokens[0], "DUDE_ACCOUNTING\t"+start+"\t"+head+"\t") {
		return out, fmt.Errorf("publication accounting evidence is incomplete")
	}
	tokens = tokens[1 : len(tokens)-2]
	seen := map[string]bool{}
	out.ByPath, out.binary = map[string]int{}, map[string]bool{}
	unreadable := fmt.Errorf("publication accounting returned an unreadable record")
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		switch {
		case t == "DUDE_SEGMENT" || t == "DUDE_MERGE":
		case strings.HasPrefix(t, ":"):
			// :<mode> <mode> <sha> <sha> <status>, then one path, or two
			// for a rename or copy.
			f := strings.Fields(t)
			if len(f) != 5 || i+1 >= len(tokens) {
				return accounting{}, unreadable
			}
			n := 1
			if f[4][0] == 'R' || f[4][0] == 'C' {
				n = 2
			}
			for range n {
				i++
				if i >= len(tokens) || tokens[i] == "" {
					return accounting{}, unreadable
				}
				seen[tokens[i]] = true
			}
		default:
			// <added>\t<deleted>\t<path>, or with an empty path, a rename's
			// two paths in the next records.
			p := strings.SplitN(t, "\t", 3)
			if len(p) != 3 {
				return accounting{}, unreadable
			}
			path := p[2]
			if path == "" {
				i += 2
				if i >= len(tokens) || tokens[i-1] == "" || tokens[i] == "" {
					return accounting{}, unreadable
				}
				path = tokens[i]
			}
			for _, n := range p[:2] {
				if n == "-" {
					out.Binary, out.binary[path] = true, true
					continue
				}
				v, err := strconv.Atoi(n)
				if err != nil || v < 0 {
					return accounting{}, unreadable
				}
				out.Lines += v
				out.ByPath[path] += v
			}
		}
	}
	out.Paths = make([]string, 0, len(seen))
	for p := range seen {
		out.Paths = append(out.Paths, p)
	}
	slices.Sort(out.Paths)
	return out, nil
}

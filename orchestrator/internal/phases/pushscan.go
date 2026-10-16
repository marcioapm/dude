package phases

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/redact"
)

// No secret in a push. lux pushes a Run's checkout to its own branch on
// GitHub, and GitHub has it from then on, so a Run given project secrets
// has everything a push sends read before dude asks lux to push, and
// scanned for every form of every value the Run was given (redact):
//   - every commit lux would send (base..HEAD, merges against each
//     parent): its message, author and committer, and each line it adds,
//     so a value committed and deleted again is still found (historyScript);
//   - the uncommitted work, as the live diff reads it (diffScript);
//   - the name of the branch lux pushes to.
//
// One found refuses the push for good: the Run fails, kept so the owner
// may resume it once the value is out of its history, saying which secret
// and where, never the value. Binary files' contents are not scanned: git
// prints no lines for them.

// EvPushRefused names the secrets found in a Run's work and where, never
// their values. Payload: {secrets, files}.
const EvPushRefused = "run.push.refused"

// SecretInWork is one secret found in what a push would send: a file, a
// commit's message or author, or the branch name.
type SecretInWork struct{ Secret, File string }

// hits collects SecretInWork once each, in the order found.
type hits []SecretInWork

func (h *hits) add(secret, where string) {
	hit := SecretInWork{secret, where}
	if !slices.Contains(*h, hit) {
		*h = append(*h, hit)
	}
}

// scanPatch adds the secrets red finds in patch's added lines, each named
// by its file through where.
func (h *hits) scanPatch(red *redact.Redactor, patch string, where func(file string) string) {
	file := ""
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			file = ""
			if _, b, ok := strings.Cut(line, " b/"); ok {
				file = b
			}
			continue
		case strings.HasPrefix(line, "+++ "):
			if p, ok := strings.CutPrefix(line, "+++ b/"); ok {
				file = p
			}
			continue
		case !strings.HasPrefix(line, "+"):
			continue
		}
		if name, ok := red.Contains(line[1:]); ok {
			h.add(name, where(file))
		}
	}
}

// scanWork finds the secrets red holds in the added lines of diffScript's
// output, once per secret and file, in the order found. A file is named as
// the live diff names it: with its repository when there are several.
func scanWork(red *redact.Redactor, text string) []SecretInWork {
	var found hits
	sections := splitDiff(text)
	for _, sec := range sections {
		found.scanPatch(red, sec.Text, func(file string) string {
			if len(sections) > 1 {
				return sec.Repo + "/" + file
			}
			return file
		})
	}
	return found
}

// historyScript prints, per repository given as diffScript's are, every
// commit from where the checkout started to HEAD: what lux pushes. Each
// section starts "# dude-history <name> <base>"; each commit
// "\x00commit <sha>", then its author's and committer's name and email a
// line each, its whole message, "\x00", and its patch (a merge's against
// each parent). Read-only POSIX sh. It exits 4 when a repository's
// checkout, base or history cannot be read rather than leaving it out, and
// prints at most historyLimit+1 bytes.
const historyScript = `set -u
base_of() {
  if first=$(git -C "$1" reflog show --format=%H "refs/heads/$2" -- 2>/dev/null | tail -n 1) && [ -n "$first" ]; then
    echo "$first"
  else
    git -C "$1" rev-parse -q --verify "$2^{commit}" 2>/dev/null || git -C "$1" rev-parse -q --verify "origin/$2^{commit}"
  fi
}
out=$(mktemp) || exit 3
status=0
while [ $# -ge 3 ]; do
  name=$1 dir=$2 ref=$3; shift 3
  [ -d "$dir" ] || { echo "$name: no checkout at $dir" >&2; status=4; continue; }
  if ! base=$(base_of "$dir" "$ref"); then
    echo "$name: no commit $ref" >&2; status=4; continue
  fi
  echo "# dude-history $name $base" >> "$out"
  git -C "$dir" -c core.quotePath=off log -p -m --no-color --no-ext-diff --no-textconv \
    --format='%x00commit %H%n%an%n%ae%n%cn%n%ce%n%B%x00' "$base..HEAD" -- >> "$out" || status=4
done
head -c ` + historyLimitText + ` "$out"
rm -f "$out"
exit $status`

// historyLimit is the most history the scan reads (below lux.ExecLimit, so
// nothing is dropped unseen); a push with more is refused as unchecked.
const (
	historyLimit     = 7 << 20
	historyLimitText = "7340033" // historyLimit+1
)

func historyCommand(bases map[string]string) []string {
	cmd := []string{"sh", "-c", historyScript, "dude-history"}
	for _, name := range slices.Sorted(maps.Keys(bases)) {
		cmd = append(cmd, name, RepoPath(name), bases[name])
	}
	return cmd
}

// scanHistory finds the secrets red holds in historyScript's output: in a
// commit's message, author or committer, named by its short sha, and in
// the lines it adds, named by file as scanWork names them.
func scanHistory(red *redact.Redactor, text string) []SecretInWork {
	var found hits
	sections := strings.Split("\n"+text, "\n# dude-history ")[1:]
	for _, part := range sections {
		head, body, _ := strings.Cut(part, "\n")
		repo, _, _ := strings.Cut(head, " ")
		in := func(where string) string {
			if len(sections) > 1 {
				return where + " in " + repo
			}
			return where
		}
		for _, commit := range strings.Split(body, "\x00commit ")[1:] {
			meta, patch, _ := strings.Cut(commit, "\x00")
			lines := strings.SplitN(meta, "\n", 6)
			for len(lines) < 6 {
				lines = append(lines, "")
			}
			short := lines[0][:min(len(lines[0]), 7)]
			if name, ok := red.Contains(lines[1] + " <" + lines[2] + ">"); ok {
				found.add(name, in("commit author of "+short))
			}
			if name, ok := red.Contains(lines[3] + " <" + lines[4] + ">"); ok {
				found.add(name, in("commit committer of "+short))
			}
			if name, ok := red.Contains(lines[5]); ok {
				found.add(name, in("commit message of "+short))
			}
			found.scanPatch(red, patch, func(file string) string {
				if len(sections) > 1 {
					return repo + "/" + file
				}
				return file
			})
		}
	}
	return found
}

// missingHistory are the repositories of bases historyScript's output has
// no section for, in name order.
func missingHistory(text string, bases map[string]string) []string {
	seen := map[string]bool{}
	for _, part := range strings.Split("\n"+text, "\n# dude-history ")[1:] {
		head, _, _ := strings.Cut(part, "\n")
		repo, _, _ := strings.Cut(head, " ")
		seen[repo] = true
	}
	var missing []string
	for _, name := range slices.Sorted(maps.Keys(bases)) {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

// scanBeforePush reads what a push of the Run would send and finds its
// secrets in it; nothing for a Run given none.
func (s *Syncer) scanBeforePush(ctx context.Context, r phaseRun) ([]SecretInWork, error) {
	var red *redact.Redactor
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) (err error) {
		red, err = ledger.Redactor(ctx, tx, r.ID)
		return err
	}); err != nil || red.Empty() {
		return nil, err
	}
	if len(r.BaseSHAs) == 0 {
		return nil, errNoBase
	}
	ctx, cancel := context.WithTimeout(ctx, diffTimeout)
	defer cancel()
	work, err := s.execChecked(ctx, r, diffCommand("-", r.BaseSHAs), "the diff")
	if err != nil {
		return nil, err
	}
	history, err := s.execChecked(ctx, r, historyCommand(r.BaseSHAs), "the history")
	if exit := (execExit{}); errors.As(err, &exit) && exit.code == 4 {
		return nil, errUnchecked{exit.Error()}
	}
	if err != nil {
		return nil, err
	}
	if len(history) > historyLimit {
		return nil, errUnchecked{fmt.Sprintf("the commits to push are over %d MiB, more than dude reads", historyLimit>>20)}
	}
	// Exit 0 is not enough: every repository lux would push has its section.
	if missing := missingHistory(history, r.BaseSHAs); len(missing) > 0 {
		return nil, errUnchecked{"no history was read for " + strings.Join(missing, ", ")}
	}
	var found hits
	for _, h := range slices.Concat(scanWork(red, work), scanHistory(red, history)) {
		found.add(h.Secret, h.File)
	}
	branch := r.PushBranch
	if branch == "" {
		branch = runBranch(r)
	}
	if name, ok := red.Contains(branch); ok {
		found.add(name, "the branch name")
	}
	return found, nil
}

// execChecked runs command in the Run's container: its output, or an error
// saying what exited how.
func (s *Syncer) execChecked(ctx context.Context, r phaseRun, command []string, what string) (string, error) {
	res, err := s.Lux.Exec(ctx, r.LuxRunID, command)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", execExit{what, res.ExitCode, strings.TrimSpace(string(res.Stderr))}
	}
	return string(res.Stdout), nil
}

// execExit is a command that ran and exited non-zero.
type execExit struct {
	what   string
	code   int
	stderr string
}

func (e execExit) Error() string { return fmt.Sprintf("%s exited %d: %s", e.what, e.code, e.stderr) }

// errUnchecked: what a push would send cannot be read whole, and is not
// pushed unchecked. Not retried: reading it again reads the same.
type errUnchecked struct{ why string }

func (e errUnchecked) Error() string {
	return "dude cannot check the work for secrets before pushing it: " + e.why
}

var errNoBase = fmt.Errorf("lux never reported where the checkout started, so dude cannot check the work for secrets before pushing it")

// refusePush fails the Run for the secrets found in its work, with one
// event naming them, and keeps it for a person to resume.
func (s *Syncer) refusePush(ctx context.Context, r phaseRun, found []SecretInWork) error {
	var secrets, files, said []string
	for _, f := range found {
		if !slices.Contains(secrets, f.Secret) {
			secrets = append(secrets, f.Secret)
		}
		if !slices.Contains(files, f.File) {
			files = append(files, f.File)
		}
		said = append(said, f.Secret+" in "+f.File)
	}
	reason := "dude did not push this work: it contains the project secret " + strings.Join(said, ", ") +
		". Take it out of the files and their commits, then resume the agent or retry the step."
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) error {
		return s.event(ctx, tx, r, EvPushRefused, ledger.ActorSystem, map[string]any{"secrets": secrets, "files": files})
	}); err != nil {
		return err
	}
	s.Log.Warn("refused to push a Run's work holding a project secret", "run", r.ID, "secrets", secrets, "files", files)
	return s.failKept(ctx, r, reason)
}

// pushScanned reports whether lux may be asked to push r's work: false
// once the Run has been failed for a secret in it.
func (s *Syncer) pushScanned(ctx context.Context, r phaseRun) (bool, error) {
	found, err := s.scanBeforePush(ctx, r)
	var unchecked errUnchecked
	if err == errNoBase || errors.As(err, &unchecked) {
		return false, s.failKept(ctx, r, err.Error())
	}
	if le, ok := lux.AsError(err); ok && le.Code == "not_running" {
		return false, s.failKept(ctx, r, "the agent's container stopped before its work was checked and pushed: "+le.Message)
	}
	if err != nil {
		return false, s.retryLater(ctx, r, err)
	}
	if len(found) > 0 {
		return false, s.refusePush(ctx, r, found)
	}
	return true, nil
}

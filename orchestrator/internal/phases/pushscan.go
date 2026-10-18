package phases

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/redact"
)

// No secret in a push. lux pushes a Run's checkout to its own branch on
// GitHub, and GitHub has it from then on, so a Run given project secrets
// has everything a push sends read before dude asks lux to push, and
// scanned for every form of every value the Run was given (redact):
//   - every commit lux would send (base..HEAD, less what the Run's trusted
//     base already has: that is on GitHub): its message, author and
//     committer, and each line it adds, a merge's being its resolution, so
//     a value committed and deleted again is still found (historyScript);
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
// by its file after prefix.
func (h *hits) scanPatch(red *redact.Redactor, patch, prefix string) {
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
			h.add(name, prefix+file)
		}
	}
}

// repoPrefix is what a file of repo is named after, as the live diff names
// it: its repository when there are several.
func repoPrefix(repo string, sections int) string {
	if sections > 1 {
		return repo + "/"
	}
	return ""
}

// scanWork adds the secrets red holds in the added lines of diffScript's
// output, once per secret and file, in the order found.
func (h *hits) scanWork(red *redact.Redactor, text string) {
	sections := splitDiff(text)
	for _, sec := range sections {
		h.scanPatch(red, sec.Text, repoPrefix(sec.Repo, len(sections)))
	}
}

// historyScript prints, per repository given as diffScript's are followed
// by its trusted base ("" for none), every commit from where the checkout
// started to HEAD that the trusted base does not contain: what lux pushes
// that GitHub lacks. Each section starts "# dude-history <name> <base>";
// each commit "\x00commit <sha>", then its author's and committer's name
// and email a line each, its whole message, "\x00", and its patch. A
// two-parent merge's patch is its remerge-diff (what the resolution
// changed from git's own merge of its parents); a git without remerge-diff
// (before 2.36) and an octopus merge, which remerge-diff skips, print one
// against each parent instead. A trusted base the checkout lacks excludes
// nothing. Read-only POSIX sh.
//
// The log is piped through head, so at most historyLimit+1 bytes are ever
// held; past that, head closes the pipe and git dies of SIGPIPE (141). POSIX
// sh has no pipefail, so each repository's outcome goes to a status file:
// "fail" for a checkout, base or log that cannot be read (exit 4), "cut"
// for a log stopped by the closed pipe, and "done" once every repository
// was read; without "done", or with "cut", the history was cut short at
// the limit (exit 5).
const historyScript = `set -u
base_of() {
  if first=$(git -C "$1" reflog show --format=%H "refs/heads/$2" -- 2>/dev/null | tail -n 1) && [ -n "$first" ]; then
    echo "$first"
  else
    git -C "$1" rev-parse -q --verify "$2^{commit}" 2>/dev/null || git -C "$1" rev-parse -q --verify "origin/$2^{commit}"
  fi
}
st=$(mktemp) || exit 3
{
  while [ $# -ge 4 ]; do
    name=$1 dir=$2 ref=$3 trusted=$4; shift 4
    if [ ! -d "$dir" ]; then
      echo "$name: no checkout at $dir" >&2; echo fail >> "$st"; continue
    fi
    if ! base=$(base_of "$dir" "$ref"); then
      echo "$name: no commit $ref" >&2; echo fail >> "$st"; continue
    fi
    not=
    if [ -n "$trusted" ] && git -C "$dir" rev-parse -q --verify "$trusted^{commit}" > /dev/null 2>&1; then
      not=$trusted
    fi
    merges=--diff-merges=remerge
    if ! git -C "$dir" log -n 0 --diff-merges=remerge HEAD -- > /dev/null 2>&1 ||
      [ -n "$(git -C "$dir" rev-list -n 1 --min-parents=3 "$base..HEAD" ${not:+--not "$not"} -- 2>/dev/null)" ]; then
      merges=-m
    fi
    echo "# dude-history $name $base"
    git -C "$dir" -c core.quotePath=off log -p $merges --no-color --no-ext-diff --no-textconv \
      --format='%x00commit %H%n%an%n%ae%n%cn%n%ce%n%B%x00' "$base..HEAD" ${not:+--not "$not"} --
    case $? in
      0) ;;
      141) echo cut >> "$st" ;;
      *) echo fail >> "$st" ;;
    esac
  done
  echo done >> "$st"
} | head -c ` + historyLimitText + `
grep -qx fail "$st"; failed=$?
grep -qx done "$st" && ! grep -qx cut "$st"; whole=$?
rm -f "$st"
[ $failed -ne 0 ] || exit 4
[ $whole -eq 0 ] || exit 5`

// historyLimit is the most history the scan reads; a push with more is
// refused as unchecked. A constant, not a setting: it must stay below
// lux.ExecLimit (8 MiB) so that nothing is dropped unseen.
const (
	historyLimit     = 7 << 20
	historyLimitText = "7340033" // historyLimit+1
)

// historyCommand reads the history of each repository of bases (name → the
// commit or branch its checkout started from), less its trusted base when
// it has one (name → sha).
func historyCommand(bases, trusted map[string]string) []string {
	cmd := []string{"sh", "-c", historyScript, "dude-history"}
	for _, name := range slices.Sorted(maps.Keys(bases)) {
		cmd = append(cmd, name, RepoPath(name), bases[name], trusted[name])
	}
	return cmd
}

// trustedBases is, per repository the Run's checkout started in, the commit
// of its base branch lux last reported fetching for the Run
// (runs.synced_bases): GitHub had it, so what it contains is not the Run's
// to send. A repository with none is absent.
func trustedBases(ctx context.Context, tx pgx.Tx, r phaseRun) (map[string]string, error) {
	var synced map[string]map[string]string
	if err := tx.QueryRow(ctx, `SELECT synced_bases FROM runs WHERE id = $1`, r.ID).Scan(&synced); err != nil || len(synced) == 0 {
		return nil, err
	}
	tb, err := delivery.TaskBranchTx(ctx, tx, r.TaskID, r.Attempt)
	if err != nil {
		return nil, err
	}
	repos, err := delivery.TaskRepositories(ctx, tx, r.TaskID)
	if err != nil {
		return nil, err
	}
	byName := repositoriesBySpecName(repos)
	out := map[string]string{}
	for name := range r.BaseSHAs {
		repo, ok := byName[name]
		if !ok {
			continue
		}
		if sha := synced[name][baseBranch(tb, repo)]; isSHA(sha) {
			out[name] = sha
		}
	}
	return out, nil
}

// baseBranch is the branch repo's task work is based on.
func baseBranch(tb delivery.TaskBranch, repo delivery.Repository) string {
	return cmp.Or(tb.Bases[repo.Name], repo.DefaultBranch)
}

// splitHistory splits historyScript's output into its repositories'
// sections, as splitDiff does diffScript's.
func splitHistory(text string) []diffSection {
	var out []diffSection
	for _, part := range strings.Split("\n"+text, "\n# dude-history ")[1:] {
		head, body, _ := strings.Cut(part, "\n")
		repo, base, _ := strings.Cut(head, " ")
		out = append(out, diffSection{Repo: repo, Base: base, Text: body})
	}
	return out
}

// scanHistory adds the secrets red holds in historyScript's output: in a
// commit's message, author or committer, named by its short sha, and in
// the lines it adds, named by file as scanWork names them.
func (h *hits) scanHistory(red *redact.Redactor, text string) {
	sections := splitHistory(text)
	for _, sec := range sections {
		in := func(where string) string {
			if len(sections) > 1 {
				return where + " in " + sec.Repo
			}
			return where
		}
		for _, commit := range strings.Split(sec.Text, "\x00commit ")[1:] {
			meta, patch, _ := strings.Cut(commit, "\x00")
			lines := strings.SplitN(meta, "\n", 6)
			for len(lines) < 6 {
				lines = append(lines, "")
			}
			short := lines[0][:min(len(lines[0]), 7)]
			if name, ok := red.Contains(lines[1] + " <" + lines[2] + ">"); ok {
				h.add(name, in("commit author of "+short))
			}
			if name, ok := red.Contains(lines[3] + " <" + lines[4] + ">"); ok {
				h.add(name, in("commit committer of "+short))
			}
			if name, ok := red.Contains(lines[5]); ok {
				h.add(name, in("commit message of "+short))
			}
			h.scanPatch(red, patch, repoPrefix(sec.Repo, len(sections)))
		}
	}
}

// missingHistory are the repositories of bases historyScript's output has
// no section for, in name order.
func missingHistory(text string, bases map[string]string) []string {
	seen := map[string]bool{}
	for _, sec := range splitHistory(text) {
		seen[sec.Repo] = true
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
	var trusted map[string]string
	if err := s.DB.InOrg(ctx, r.Org, func(tx pgx.Tx) (err error) {
		if red, err = ledger.Redactor(ctx, tx, r.ID); err != nil || red.Empty() {
			return err
		}
		trusted, err = trustedBases(ctx, tx, r)
		return err
	}); err != nil || red.Empty() {
		return nil, err
	}
	if len(r.BaseSHAs) == 0 {
		return nil, errNoBase
	}
	ctx, cancel := context.WithTimeout(ctx, diffTimeout)
	defer cancel()
	// Against where each checkout started, never narrowed by what the Run
	// was given: a narrower base would hide added lines from the scan.
	work, err := s.execChecked(ctx, r, diffCommand("-", r.BaseSHAs, nil), "the diff")
	if err != nil {
		return nil, err
	}
	history, err := s.execChecked(ctx, r, historyCommand(r.BaseSHAs, trusted), "the history")
	exit := execExit{}
	switch {
	case errors.As(err, &exit) && exit.code == 4:
		return nil, errUnchecked{exit.Error()}
	case errors.As(err, &exit) && exit.code == 5, err == nil && len(history) > historyLimit:
		return nil, errUnchecked{fmt.Sprintf("the commits to push are over %d MiB, more than dude reads", historyLimit>>20)}
	case err != nil:
		return nil, err
	}
	// Exit 0 is not enough: every repository lux would push has its section.
	if missing := missingHistory(history, r.BaseSHAs); len(missing) > 0 {
		return nil, errUnchecked{"no history was read for " + strings.Join(missing, ", ")}
	}
	var found hits
	found.scanWork(red, work)
	found.scanHistory(red, history)
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

package phases

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
	"github.com/marciomartins/dude/orchestrator/internal/redact"
)

// No secret in a push. lux pushes a Run's checkout to its own branch on
// GitHub, and GitHub has it from then on, so a Run given project secrets
// has its checkout read before dude asks lux to push: the diff script's
// output (what the live diff reads: commits and uncommitted work against
// where the checkout started) scanned for every form of every value the
// Run was given (redact). One found refuses the push for good: the Run
// fails, kept so the owner may resume it once the value is out of its
// history, saying which secret and file, never the value. Binary files'
// contents are not scanned: git prints no lines for them.

// EvPushRefused names the secrets found in a Run's work and where, never
// their values. Payload: {secrets, files}.
const EvPushRefused = "run.push.refused"

// SecretInWork is one secret found in a file of the work to be pushed.
type SecretInWork struct{ Secret, File string }

// scanWork finds the secrets red holds in the added lines of diffScript's
// output, once per secret and file, in the order found. A file is named as
// the live diff names it: with its repository when there are several.
func scanWork(red *redact.Redactor, text string) []SecretInWork {
	var found []SecretInWork
	sections := splitDiff(text)
	for _, sec := range sections {
		file := ""
		for _, line := range strings.Split(sec.Text, "\n") {
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
			name, ok := red.Contains(line[1:])
			if !ok {
				continue
			}
			where := file
			if len(sections) > 1 {
				where = sec.Repo + "/" + file
			}
			hit := SecretInWork{name, where}
			if !slices.Contains(found, hit) {
				found = append(found, hit)
			}
		}
	}
	return found
}

// scanBeforePush reads the Run's checkout and finds its secrets in it;
// nothing for a Run given none.
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
	res, err := s.Lux.Exec(ctx, r.LuxRunID, diffCommand("-", r.BaseSHAs))
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("the diff exited %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return scanWork(red, string(res.Stdout)), nil
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
	if err == errNoBase {
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

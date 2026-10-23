package phases

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// checkLogDir is where a fix Run's init writes its failing checks' job
// log tails, under its workspace.
const checkLogDir = "/.dude/checks"

// checkLogMin is the least of a log worth a file: below it the prompt's
// excerpt says as much.
const checkLogMin = 4 << 10

// checkLogsInit writes the failing checks' job log tails (FailingCheck.Full,
// at most delivery.CheckLogFiles of them, in feedback order) into
// dir/.dude/checks, as instructionsInit writes the instruction file: each
// in a quoted here-document whose delimiter is derived from its text,
// written beside its place and renamed into it. Its script, an && list to
// follow the instructions' write, takes at most room bytes with its " &&\n"
// joint: past it, the longest logs are shortened to a fair share each, and
// when a share would be under checkLogMin the last logs get no file.
//
// It returns a copy of feedback whose checks say where their file is
// (Path, FileKiB, FileCut), or that none was written (NoFile), for the
// prompt to name. Nothing in feedback is changed.
func checkLogsInit(dir string, feedback []forge.ActionableFeedback, room int) ([]forge.ActionableFeedback, string) {
	out := slices.Clone(feedback)
	type file struct {
		check      *forge.FailingCheck
		path, body string
	}
	var files []file
	taken := map[string]bool{}
	for i := range out {
		out[i].Checks = slices.Clone(out[i].Checks)
		for j := range out[i].Checks {
			c := &out[i].Checks[j]
			if c.Full == "" {
				continue
			}
			if len(files) == delivery.CheckLogFiles {
				c.NoFile = true
				continue
			}
			files = append(files, file{check: c, path: checkLogPath(dir, out[i].Repo, c.Name, taken), body: c.Full})
		}
	}
	if len(files) == 0 {
		return out, ""
	}
	mkdir := fmt.Sprintf("mkdir -p %s%s", dir, checkLogDir)
	for n := len(files); n > 0; n-- {
		// What the script costs besides the logs' text: each text's
		// delimiter is 31 bytes whatever the text, so this is exact.
		fixed := len(" &&\n") + len(mkdir)
		for _, f := range files[:n] {
			fixed += len(" &&\n") + len(writeFile(f.path, "", "DUDE_CHECKLOG_0123456789abcdef"))
		}
		shares := fairShares(files[:n], room-fixed, func(f file) int { return len(f.body) })
		if shares == nil {
			files[n-1].check.NoFile = true
			continue
		}
		parts := []string{mkdir}
		for k, f := range files[:n] {
			body := f.body
			if shares[k] < len(body) {
				body = delivery.LogEnd(body, shares[k])
				f.check.FileCut = true
			}
			body = strings.TrimSuffix(body, "\n") + "\n"
			sum := sha256.Sum256([]byte(body))
			end := "DUDE_CHECKLOG_" + hex.EncodeToString(sum[:8])
			if refuseDelimiter(body, end) != nil {
				f.check.NoFile, f.check.FileCut = true, false
				continue
			}
			f.check.Path = workspaceDir + strings.TrimPrefix(f.path, dir)
			f.check.FileKiB = max((len(body)+512)>>10, 1)
			parts = append(parts, writeFile(f.path, body, end))
		}
		if len(parts) == 1 {
			return out, ""
		}
		return out, strings.Join(parts, " &&\n")
	}
	return out, ""
}

// fairShares gives each item at most its size, out of total, the smallest
// first, each an equal share of what is left; nil when an item cut short
// would get under checkLogMin (or total is under nothing).
func fairShares[T any](items []T, total int, size func(T) int) []int {
	order := make([]int, len(items))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return size(items[a]) - size(items[b]) })
	shares := make([]int, len(items))
	left := total
	for k, i := range order {
		share := left / (len(items) - k)
		if s := size(items[i]) + 1; s <= share {
			share = s
		} else if share < checkLogMin {
			return nil
		}
		// One byte of each share is the newline the here-document ends with.
		shares[i] = share - 1
		left -= share
	}
	return shares
}

// checkLogPath is dir/.dude/checks/<repo>-<check>.log, each part a slug of
// lower-case letters, digits and dashes; a name taken already gets -2, -3…
func checkLogPath(dir, repo, check string, taken map[string]bool) string {
	name := slug(check)
	if r := slug(repo); repo != "" {
		name = r + "-" + name
	}
	base := name
	for n := 2; taken[name]; n++ {
		name = fmt.Sprintf("%s-%d", base, n)
	}
	taken[name] = true
	return dir + checkLogDir + "/" + name + ".log"
}

// slug is s in lower-case letters, digits and single dashes, at most 60
// bytes; "check" for nothing left.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
		if b.Len() >= 60 {
			break
		}
	}
	if b.Len() == 0 {
		return "check"
	}
	return b.String()
}

package delivery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A conductor's verify: a report-only child (start_agent's machinery, the
// investigator's phase, publishing off) told to run checks on one commit
// and change nothing; its reply ends with the checks' results as JSON,
// which settlement keeps (child_results.verify) and the parent is given,
// each command's output quoted as external text.

// Bounds of one verification: what it may ask, and what of its report is
// kept and told.
const (
	VerifyCommandsMax = 10
	VerifyCommandMax  = 500
	VerifyPathsMax    = 50
	VerifyResultsMax  = 20
	VerifyTailMax     = 2000
)

// VerifyAsk is what verify asks: a commit of one repository (the task's
// head there when empty; refused when the task has none yet), the
// commands to run or the paths whose tests to run (the change's tests
// when neither), and a note.
type VerifyAsk struct {
	Repo, Commit string
	Commands     []string
	Paths        []string
	Note         string
}

// VerifyResult is one command's result as the verifier reported it.
type VerifyResult struct {
	Command string `json:"command"`
	Exit    int    `json:"exit"`
	Tail    string `json:"tail"`
}

// VerifyReport is a verifier's results as settlement keeps them.
type VerifyReport struct {
	Repo    string         `json:"repo"`
	Commit  string         `json:"commit"`
	Status  string         `json:"status"`
	Results []VerifyResult `json:"results"`
	// Why it failed without results: the child's own status, or that it
	// reported none.
	Reason string `json:"reason,omitempty"`
	// The commands the conductor asked for, and those of them it reported
	// no result for.
	Asked   []string `json:"asked,omitempty"`
	Missing []string `json:"missing,omitempty"`
}

// Verification statuses, and the reasons settlement gives beside the
// child's own status.
const (
	VerifyPassed = "passed"
	VerifyFailed = "failed"

	verifyNoReport   = "no-report"
	verifyIncomplete = "incomplete"
)

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

var verifyBriefTmpl = mustTemplate("contracts/verify-brief")

// VerifyStart is the start_agent input for a verification: checked, its
// brief written, its commit the child's checkout of repo.
func VerifyStart(ctx context.Context, tx pgx.Tx, ref RunRef, in VerifyAsk, key string) (StartChildInput, error) {
	commands := slices.DeleteFunc(slices.Clone(in.Commands), func(c string) bool { return strings.TrimSpace(c) == "" })
	paths := slices.DeleteFunc(slices.Clone(in.Paths), func(p string) bool { return strings.TrimSpace(p) == "" })
	switch {
	case len(commands) > VerifyCommandsMax:
		return StartChildInput{}, refusef(rVerifyTooManyCommands, VerifyCommandsMax)
	case len(paths) > VerifyPathsMax:
		return StartChildInput{}, refusef(rVerifyTooManyPaths, VerifyPathsMax)
	}
	for i, c := range commands {
		commands[i] = strings.TrimSpace(c)
		if utf8.RuneCountInString(c) > VerifyCommandMax || strings.ContainsAny(c, "\n\r") {
			return StartChildInput{}, refusef(rVerifyCommandTooLong, i+1, VerifyCommandMax)
		}
	}
	repos, err := TaskRepositories(ctx, tx, ref.TaskID)
	if err != nil {
		return StartChildInput{}, err
	}
	repo := strings.TrimSpace(in.Repo)
	if repo == "" {
		if len(repos) != 1 {
			return StartChildInput{}, refuseText(rVerifyNameRepository)
		}
		repo = repos[0].Name
	}
	i := slices.IndexFunc(repos, func(r Repository) bool { return r.Name == repo })
	if i < 0 {
		return StartChildInput{}, refusef(rVerifyNoSuchRepository, repo)
	}
	commit := strings.ToLower(strings.TrimSpace(in.Commit))
	if commit != "" && !fullSHA.MatchString(commit) {
		return StartChildInput{}, refusef(rVerifyCommitNotSHA, in.Commit)
	}
	if commit == "" {
		d, err := ReadDelivery(ctx, tx, ref.TaskID)
		if err != nil {
			return StartChildInput{}, err
		}
		if d != nil {
			commit = d.State.Heads[repo]
		}
		if commit == "" {
			return StartChildInput{}, refusef(rVerifyNoCommit, repo)
		}
	}
	brief := render(verifyBriefTmpl, struct {
		Repo, Path, Commit string
		Commands, Paths    []string
		ResultsMax         int
	}{repo, "/workspace/repos/" + lux.SpecName(repo), commit, commands, paths, VerifyResultsMax})
	return StartChildInput{Parent: ChildParent{Kind: ChildParentTaskConductor, Run: ref}, Profile: PromptRoleForPhase[PhaseInvestigate],
		Prompt: brief, Note: in.Note, Key: key, Capabilities: ChildCapabilities{Verify: &VerifyTarget{Repo: repo, Commit: commit, Commands: commands}},
		BaseRefs: map[string]string{repo: commit}}, nil
}

// fencedJSON is a ```json fenced block's body.
var fencedJSON = regexp.MustCompile("(?s)```json[ \t]*\n(.*?)\n```")

// ParseVerify reads the verifier's results from its reply: the last
// ```json block holding {"verify": [...]}, each command and its output's
// tail cut to VerifyCommandMax characters and VerifyTailMax bytes, at most
// VerifyResultsMax. ok is false for a reply with no such block.
func ParseVerify(reply string) (results []VerifyResult, ok bool) {
	blocks := fencedJSON.FindAllStringSubmatch(reply, -1)
	for i := len(blocks) - 1; i >= 0; i-- {
		var doc struct {
			Verify *[]VerifyResult `json:"verify"`
		}
		if json.Unmarshal([]byte(blocks[i][1]), &doc) != nil || doc.Verify == nil {
			continue
		}
		out := []VerifyResult{}
		for _, r := range *doc.Verify {
			if len(out) == VerifyResultsMax {
				break
			}
			out = append(out, VerifyResult{Command: clip(oneLine(r.Command), VerifyCommandMax), Exit: r.Exit, Tail: tail(r.Tail, VerifyTailMax)})
		}
		return out, true
	}
	return nil, false
}

// tail is the last n bytes of s, cut at a rune's start.
func tail(s string, n int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return "…" + s[i:]
}

// verifyReportTx is a settling verifier's report on target: passed only
// when it completed, reported a result for every command it was asked to
// run (matched to its asked command on one line, as ParseVerify reads it), and every result exited 0.
func verifyReportTx(ctx context.Context, tx pgx.Tx, childID, status string, target VerifyTarget) (VerifyReport, error) {
	rep := VerifyReport{Repo: target.Repo, Commit: target.Commit, Status: VerifyFailed, Results: []VerifyResult{}, Asked: target.Commands}
	reply, err := RunReplyTx(ctx, tx, childID)
	if err != nil {
		return rep, err
	}
	results, ok := ParseVerify(reply)
	for _, c := range target.Commands {
		line := oneLine(c)
		if !slices.ContainsFunc(results, func(r VerifyResult) bool { return r.Command == line }) {
			rep.Missing = append(rep.Missing, c)
		}
	}
	switch {
	case status != "completed":
		rep.Reason = status
	case !ok || len(results) == 0:
		rep.Reason = verifyNoReport
	case len(rep.Missing) > 0:
		rep.Reason = verifyIncomplete
	}
	if ok {
		rep.Results = results
	}
	if rep.Reason == "" && !slices.ContainsFunc(results, func(r VerifyResult) bool { return r.Exit != 0 }) {
		rep.Status = VerifyPassed
	}
	return rep, nil
}

var verifyResultTmpl = mustTemplate("contracts/verify-result")

// toldResult is a result as the parent is told it: the command it asked
// for that the result reports, if any, and the verifier's own text for
// it quoted as external text, as its output is.
type toldResult struct {
	Asked   string `json:"asked,omitempty"`
	Command string `json:"command"`
	Exit    int    `json:"exit"`
	Tail    string `json:"tail"`
	// The summary's line for it: Asked, or the reported command clipped
	// and quoted.
	Line string `json:"-"`
}

// verifyResultText is what the parent is told of a verifier's report: its
// verdict and each command's exit code in words, then the report as JSON,
// every reported command and output's tail quoted as external text under
// one random id (the verifier wrote the one, the repository's code the
// other).
func verifyResultText(childID string, raw []byte) (string, error) {
	var rep VerifyReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		return "", err
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	id := hex.EncodeToString(b[:])
	told := make([]toldResult, len(rep.Results))
	passed := 0
	for i, r := range rep.Results {
		t := toldResult{Command: ExternalText(id, r.Command), Exit: r.Exit, Tail: ExternalText(id, r.Tail),
			Line: ExternalText(id, clip(r.Command, 200))}
		if j := slices.IndexFunc(rep.Asked, func(a string) bool { return oneLine(a) == r.Command }); j >= 0 {
			t.Asked, t.Line = rep.Asked[j], clip(rep.Asked[j], 200)
		}
		told[i] = t
		if r.Exit == 0 {
			passed++
		}
	}
	body, err := json.Marshal(struct {
		External string `json:"external"`
		Child    string `json:"verifyRunId"`
		VerifyReport
		Results []toldResult `json:"results"`
	}{render(verifyExternalTmpl, ExternalText(id, "…")), childID, rep, told})
	if err != nil {
		return "", err
	}
	return render(verifyResultTmpl, struct {
		Child string
		VerifyReport
		Results []toldResult
		Passed  int
		JSON    string
	}{childID, rep, told, passed, string(body)}), nil
}

var verifyExternalTmpl = mustTemplate("contracts/verify-external")

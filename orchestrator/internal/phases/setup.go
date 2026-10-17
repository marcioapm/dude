package phases

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// SetupTimeout bounds one repository's setup script. lux puts no bound on
// init, so the composed script does, with coreutils' timeout.
const SetupTimeout = 15 * time.Minute

// Setup is a repository's setup script and where it is checked out.
type Setup struct {
	// The repository's name in the spec (lux.SpecName): what the failure
	// line names.
	Name, Path, Script string
}

// setupFailedPrefix starts the line the composed script writes last when
// a repository's setup script fails: "<prefix><exit code> <name>".
const setupFailedPrefix = "dude-setup-failed: "

// setupRunningPrefix starts the line init writes before each script.
const setupRunningPrefix = "dude: running the setup script of "

// setupFunc runs one repository's setup script (dude_setup PATH BASE64):
// in the checkout's root, once per checkout, with bash -e and stdin
// closed, under SetupTimeout. Its marker, under the checkout's git dir and
// so never in its work tree, is written only on success: a resume of the
// same checkout skips the script, a fresh checkout runs it. The script
// runs from a file beside the marker, decoded from base64. The
// command-scope git settings every Run carries (workloadGitConfig) are
// cleared for it, so a hook installer writes the repository's own config
// and hooks directory, which dude's hook then runs. A subshell: the cd and
// the environment end with it.
var setupFunc = `dude_setup() (
	cd "$1" || exit
	done=$(git rev-parse --git-path dude-setup-done) && script=$(git rev-parse --git-path dude-setup.sh) || exit
	[ -e "$done" ] && exit 0
	printf '%s' "$2" | base64 -d > "$script" || exit
	for v in $(env | sed -n 's/^\(GIT_CONFIG_\(KEY\|VALUE\)_[0-9]*\)=.*/\1/p'); do unset "$v"; done
	unset GIT_CONFIG_PARAMETERS
	export GIT_CONFIG_COUNT=0
	timeout -k 10s ` + strconv.Itoa(int(SetupTimeout.Seconds())) + `s bash -e "$script" < /dev/null
	code=$?
	rm -f "$script"
	[ "$code" -eq 0 ] || exit "$code"
	: > "$done"
)
`

// SetupInit is the init script that runs every repository's setup script
// in its checkout, in order, stopping at the first that fails: init then
// exits with its code, having said which on stderr (setupFailedPrefix),
// and lux fails the Run init-failed. "" when none has one. A name, path
// and script reach the shell only single-quoted or as base64, never as
// code.
func SetupInit(setups []Setup) string {
	var b strings.Builder
	for _, s := range setups {
		if strings.TrimSpace(s.Script) == "" {
			continue
		}
		if b.Len() == 0 {
			b.WriteString(setupFunc)
		}
		name := shellQuote(s.Name)
		b.WriteString("echo \"" + setupRunningPrefix + "\"" + name + " >&2\n")
		b.WriteString("dude_setup " + shellQuote(s.Path) + " " + base64.StdEncoding.EncodeToString([]byte(s.Script)) +
			" || { code=$?; echo \"" + setupFailedPrefix + "$code \"" + name + " >&2; exit \"$code\"; }\n")
	}
	return b.String()
}

// InitOutput is what a Run's init printed in its current placement, from
// lux's lux.init start to its done, kept to its last initOutputMax bytes,
// and the setup script that failed in it. Stored in harness_state, so it
// outlives a batch.
type InitOutput struct {
	Running bool          `json:"running,omitempty"`
	Tail    string        `json:"tail,omitempty"`
	Failed  *SetupFailure `json:"failed,omitempty"`
}

// SetupFailure is a repository's setup script that failed: which, with
// what exit code, and the end of what init printed before it said so.
type SetupFailure struct {
	Repository string `json:"repository"`
	ExitCode   int    `json:"exitCode"`
	Output     string `json:"output"`
	Epoch      int    `json:"epoch"`
}

// initOutputMax is how much of init's output a failure keeps.
const initOutputMax = 4 << 10

// Record takes one of the Run's records: lux.init's start and done
// (phase, exitCode), and while init runs, its stdout and stderr. Anything
// else is not init's.
func (o *InitOutput) Record(f lux.Frame) {
	if f.Event != nil {
		if f.Event.Type != "lux.init" {
			return
		}
		var d struct {
			Phase    string `json:"phase"`
			ExitCode int    `json:"exitCode"`
		}
		_ = json.Unmarshal(f.Event.Data, &d)
		switch d.Phase {
		case "start":
			*o = InitOutput{Running: true}
		case "done":
			o.Running = false
			if d.ExitCode != 0 {
				o.Failed = setupFailed(o.Tail, d.ExitCode, f.Epoch)
			}
			o.Tail = ""
		}
		return
	}
	if !o.Running || (f.Channel != "stdout" && f.Channel != "stderr") {
		return
	}
	o.Tail += f.Data
	if over := len(o.Tail) - initOutputMax; over > 0 {
		o.Tail = strings.ToValidUTF8(o.Tail[over:], "")
	}
}

// setupFailed reads which repository's setup script failed from the line
// the composed init writes last (setupFailedPrefix); nil when init failed
// otherwise. Output is what came before that line.
func setupFailed(tail string, code, epoch int) *SetupFailure {
	tail = strings.TrimRight(tail, "\n")
	i := strings.LastIndex(tail, setupFailedPrefix)
	if i < 0 || (i > 0 && tail[i-1] != '\n') {
		return nil
	}
	_, name, ok := strings.Cut(tail[i+len(setupFailedPrefix):], " ")
	if !ok || strings.Contains(name, "\n") {
		return nil
	}
	return &SetupFailure{Repository: name, ExitCode: code, Output: strings.TrimRight(tail[:i], "\n"), Epoch: epoch}
}

// Reason is the failure in one sentence for the people on the task: the
// repository, the exit code, and the last line its script printed.
func (f SetupFailure) Reason() string {
	why := fmt.Sprintf("The setup script of %s failed with exit code %d", f.Repository, f.ExitCode)
	if f.ExitCode == 124 {
		why = fmt.Sprintf("The setup script of %s did not finish within %s", f.Repository, SetupTimeout)
	}
	lines := strings.Split(strings.TrimSpace(f.Output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if last := strings.TrimSpace(lines[i]); last != "" && !strings.HasPrefix(last, setupRunningPrefix) {
			return why + ": " + last
		}
	}
	return why + "."
}

// SetupSpec is a spec's init for setups, nil when none has a script.
func SetupSpec(setups []Setup) *lux.Init {
	if script := SetupInit(setups); script != "" {
		return &lux.Init{Script: script}
	}
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

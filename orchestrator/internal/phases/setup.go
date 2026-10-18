package phases

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// SetupTimeout bounds one repository's setup script. lux puts no bound on
// init, so init does, with coreutils' timeout.
const SetupTimeout = 15 * time.Minute

// Setup is a repository's setup script and where it is checked out.
type Setup struct {
	// The repository's name in the spec (lux.SpecName): what the failure
	// line names.
	Name, Path, Script string
}

// setupFailedPrefix starts the line init writes last when a repository's
// setup script fails: "<prefix><exit code> <name>".
const setupFailedPrefix = "dude-setup-failed: "

// setupRunningPrefix starts the line init writes before each script.
const setupRunningPrefix = "dude: running the setup script of "

// The setup manifest: a file secret every agent Run and every preview of
// repositories carries from its submit, its value given again on every
// resume (lux keeps none), so a resume names a repository it adds. lux
// keeps the init a Run was submitted with; the manifest is what changes.
const (
	setupSecretName = "dude-setup"
	// SetupManifestPath is where the manifest is placed in the container.
	SetupManifestPath = agentHome + "/.dude/setup"
	// Its first line: never empty (lux refuses an empty secret value), and
	// skipped by init, as every line starting with # is.
	setupManifestHeader = "# dude setup scripts: a repository a line, its name, checkout and script, each base64\n"
)

// SetupManifest is the manifest for setups, in their order, a blank
// script left out. Each field is base64, so no name, path or script can
// break a line or reach the shell as code; a path under the workspace is
// given relative to it, where init reads it from.
func SetupManifest(setups []Setup) string {
	var b strings.Builder
	b.WriteString(setupManifestHeader)
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	for _, s := range setups {
		if strings.TrimSpace(s.Script) == "" {
			continue
		}
		path := strings.TrimPrefix(s.Path, workspaceDir+"/")
		b.WriteString(enc(s.Name) + " " + enc(path) + " " + enc(s.Script) + "\n")
	}
	return b.String()
}

// SetupSecret is the manifest as the Run's file secret.
func SetupSecret(setups []Setup) lux.Secret {
	return lux.Secret{Name: setupSecretName, Value: SetupManifest(setups), As: "file", Path: SetupManifestPath}
}

// DeclaresSetup reports whether a Run lux holds has the setup manifest:
// every resume must give it again.
func DeclaresSetup(stored lux.StoredSpec) bool {
	return slices.ContainsFunc(stored.Secrets, func(s lux.StoredSecret) bool { return s.Name == setupSecretName })
}

// SetupInit is the init that runs, in order, the setup script of each
// repository the manifest names, from workspace (where its relative paths
// are): in the checkout's root, once per checkout, with bash -e and stdin
// closed, stopped after timeout. Its marker, under the checkout's git dir
// and so never in its work tree, is written only on success: a later
// start of the same checkout skips the script, a fresh checkout or one a
// resume adds runs it. The first that fails stops init with its code,
// having said which on stderr (setupFailedPrefix), and lux fails the Run
// init-failed. A repository with no checkout (lux dropped it when its
// clone failed) is passed over. No manifest is no setup.
//
// The command-scope git settings every Run carries (workloadGitConfig)
// are cleared for a script, so a hook installer writes the repository's
// own config and hooks directory, which dude's hook then runs. A word of
// env's output that is no identifier (a line of a multi-line value) is
// passed over: dash would refuse to unset it and stop init. A decoded
// name or path keeps a trailing newline: each substitution ends with "."
// and loses it after.
func SetupInit(manifest, workspace string, timeout time.Duration) string {
	return `manifest=` + shellQuote(manifest) + `
[ -f "$manifest" ] || exit 0
cd ` + shellQuote(workspace) + ` || exit
dude_setup() (
	cd "$2" || exit
	done=$(git rev-parse --git-path dude-setup-done) && script=$(git rev-parse --git-path dude-setup.sh) || exit
	[ -e "$done" ] && exit 0
	printf '%s%s\n' '` + setupRunningPrefix + `' "$1" >&2
	printf '%s' "$3" | base64 -d > "$script" || exit
	for v in $(env | sed -n 's/=.*//p'); do
		case "$v" in
		*[!A-Za-z0-9_]*) ;;
		GIT_CONFIG_KEY_* | GIT_CONFIG_VALUE_*) unset "$v" ;;
		esac
	done
	unset GIT_CONFIG_PARAMETERS
	export GIT_CONFIG_COUNT=0
	timeout -k 10s ` + strconv.Itoa(int(timeout.Seconds())) + `s bash -e "$script" < /dev/null 3<&-
	code=$?
	rm -f "$script"
	[ "$code" -eq 0 ] || exit "$code"
	: > "$done"
)
while IFS=' ' read -r name path script <&3; do
	case $name in '' | '#'*) continue ;; esac
	name=$(printf '%s' "$name" | base64 -d && echo .) && path=$(printf '%s' "$path" | base64 -d && echo .) ||
		{ echo "dude: the setup manifest is unreadable" >&2; exit 1; }
	name=${name%.} path=${path%.}
	if [ ! -d "$path" ]; then
		printf 'dude: %s has no checkout at %s; its setup script is skipped\n' "$name" "$path" >&2
		continue
	fi
	dude_setup "$name" "$path" "$script" || { code=$?; printf '%s%s %s\n' '` + setupFailedPrefix + `' "$code" "$name" >&2; exit "$code"; }
done 3< "$manifest"
`
}

// SetupSpec gives a spec its setup: the fixed init and the manifest of
// setups.
func SetupSpec(spec *lux.Spec, setups []Setup) {
	spec.Init = &lux.Init{Script: SetupInit(SetupManifestPath, workspaceDir, SetupTimeout)}
	spec.Secrets = append(spec.Secrets, SetupSecret(setups))
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
	o.Tail = keepTail(o.Tail, f.Data, initOutputMax)
}

// keepTail is the last max bytes of tail+data, cut on a rune boundary,
// without copying more of either than it keeps.
func keepTail(tail, data string, max int) string {
	if len(data) >= max {
		return strings.Clone(strings.ToValidUTF8(data[len(data)-max:], ""))
	}
	if over := len(tail) + len(data) - max; over > 0 {
		tail = strings.ToValidUTF8(tail[over:], "")
	}
	return tail + data
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

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

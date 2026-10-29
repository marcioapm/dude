package phases

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// claudeImport is CLAUDE.md: Claude Code reads it from its working
// directory, OpenCode and Codex read AGENTS.md, which it imports.
const claudeImport = "@AGENTS.md\n"

// MaxInitScript bounds a spec's init.script: far over an instruction file
// (15–25 KB), far under lux's 8 MiB request body.
const MaxInitScript = 256 << 10

// instructionsInit is the POSIX sh script that writes the Run's
// instruction file, and CLAUDE.md importing it, into dir (a Run's is
// /workspace). lux runs it before the workload on every placement, from
// the spec it keeps, so every placement of the Run writes the same bytes.
//
// The text goes in a quoted here-document, so the shell expands nothing in
// it; the delimiter is derived from the text, and a text with that line in
// it is refused. A here-document ends its body with a newline, so the text
// must too. Each file is written beside its place and renamed into it, and
// each command runs only if the one before succeeded.
func instructionsInit(dir, text string) (string, error) {
	if !strings.HasSuffix(text, "\n") {
		return "", errors.New("the instruction file does not end with a newline")
	}
	sum := sha256.Sum256([]byte(text))
	end := "DUDE_AGENTS_" + hex.EncodeToString(sum[:8])
	if err := refuseDelimiter(text, end); err != nil {
		return "", err
	}
	return "set -eu\n" + writeFile(dir+"/AGENTS.md", text, end) + " &&\n" + writeFile(dir+"/CLAUDE.md", claudeImport, end) + "\n", nil
}

// refuseDelimiter refuses a text with end as one of its lines, where the
// here-document would end.
func refuseDelimiter(text, end string) error {
	for _, line := range strings.Split(text, "\n") {
		if line == end {
			return fmt.Errorf("the instruction file contains its here-document delimiter %q", end)
		}
	}
	return nil
}

// writeFile writes body to path through path.tmp, as one && list.
func writeFile(path, body, end string) string {
	return fmt.Sprintf("cat > %s.tmp <<'%s' &&\n%s%s\nmv -f %s.tmp %s", path, end, body, end, path, path)
}

// addInit runs script first, then whatever init the spec already has, only
// if script succeeded. That init runs in a subshell with the shell's
// defaults, so script's set -eu cannot change it.
func addInit(spec *lux.Spec, script string) {
	if spec.Init != nil && strings.TrimSpace(spec.Init.Script) != "" {
		script = strings.TrimSuffix(script, "\n") + " && (\nset +eu\n" + spec.Init.Script + "\n)\n"
	}
	spec.Init = &lux.Init{Script: script}
}

// runSpec is buildSpec for a Run with an instruction file: refused when
// the file cannot be written by an init, or the init is over MaxInitScript.
// A fix Run's check logs (CheckLogs, sized by checkLogsInit to fit) are
// written after the instruction file, before any other init.
func runSpec(c AgentConfig, in specInput) (lux.Spec, error) {
	spec := buildSpec(c, in)
	if in.Instructions == "" {
		return spec, nil
	}
	if in.NetworkContext {
		var rules []string
		unrestricted := spec.Network == nil || spec.Network.Unrestricted
		if spec.Network != nil {
			for _, rule := range spec.Network.Egress {
				rules = append(rules, rule.Host+rule.CIDR)
			}
		}
		in.Instructions = delivery.NetworkInstructions(unrestricted, rules) + "\n\n" + in.Instructions
	}
	script, err := instructionsInit(workspaceDir, in.Instructions)
	if err != nil {
		return lux.Spec{}, err
	}
	if in.CheckLogs != "" {
		script = strings.TrimSuffix(script, "\n") + " &&\n" + in.CheckLogs + "\n"
	}
	addInit(&spec, script)
	if len(spec.Init.Script) > MaxInitScript {
		return lux.Spec{}, fmt.Errorf("the Run's init script is %d bytes, over the %d-byte limit", len(spec.Init.Script), MaxInitScript)
	}
	return spec, nil
}

// initRoom is what is left for a Run's check logs once its instruction
// file and other init are written, under checkLogInitMax: 0 when nothing
// is.
func initRoom(c AgentConfig, in specInput) int {
	in.CheckLogs = ""
	spec, err := runSpec(c, in)
	if err != nil || spec.Init == nil || in.Instructions == "" {
		return 0
	}
	return max(min(MaxInitScript, checkLogInitMax)-len(spec.Init.Script), 0)
}

// checkLogInitMax bounds an init that carries check logs: lux's shim runs
// the script as one `sh -c` argument, which Linux caps at 128 KiB
// (MAX_ARG_STRLEN), less a margin for the shim's own wrapping.
const checkLogInitMax = 120 << 10

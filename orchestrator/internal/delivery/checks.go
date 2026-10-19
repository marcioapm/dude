package delivery

import (
	"strings"

	"go.yaml.in/yaml/v3"
)

// ChecksGated selects phases independently of whether they publish code.
var ChecksGated = map[string]bool{
	PhaseImplement: true,
	PhaseFix:       true,
	PhaseSimplify:  true,
}

const (
	CheckPass   = "pass"
	CheckFail   = "fail"
	CheckNotRun = "not-run"
)

type Check struct {
	Command  string `json:"command" yaml:"command"`
	Result   string `json:"result" yaml:"result"`
	Evidence string `json:"evidence" yaml:"evidence"`
}

// ParseChecks decodes the last checks document, never falling back to an
// earlier valid report when the last one is malformed. A report of no
// checks is valid input for the model's missing-check question.
func ParseChecks(reply string) ([]Check, bool) {
	lines := strings.Split(reply, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "checks:") {
			start = i
		}
	}
	if start < 0 {
		return nil, false
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") || line == "---" {
			end = i
			break
		}
	}
	var doc struct {
		Checks []Check `yaml:"checks"`
	}
	if yaml.Unmarshal([]byte(strings.Join(lines[start:end], "\n")), &doc) != nil || doc.Checks == nil {
		return nil, false
	}
	for _, c := range doc.Checks {
		if strings.TrimSpace(c.Command) == "" || (c.Result != CheckPass && c.Result != CheckFail && c.Result != CheckNotRun) {
			return nil, false
		}
	}
	return doc.Checks, true
}

package delivery

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"text/template"
	"text/template/parse"
)

// What dude tells agents lives in Markdown under agents/ (its README says
// how to edit it). roles/ holds each role's replaceable instructions as
// plain text; contracts/ and briefings/ are text/template files, each
// named by its path without ".md" ("contracts/commit"). A file's one
// trailing newline is not part of its text. Every file is read and parsed
// when the package initialises, and every name Go renders is resolved then
// (mustTemplate, agentTexts' keys) as is every {{template}} a file
// includes: a file that is missing, empty or does not parse panics at
// startup instead of on the first prompt that needs it.
//
//go:embed agents/roles/*.md agents/contracts agents/briefings agents/tools
var agentFiles embed.FS

var agentTemplates = mustLoad(parseAgentTemplates(agentFiles))

// The templates rendered with data, resolved at init by mustTemplate.
var (
	conductorTmpl              = mustTemplate("briefings/conductor")
	wakeFeedbackTmpl           = mustTemplate("briefings/wake-feedback")
	conductorStartsTmpl        = mustTemplate("contracts/conductor-starts")
	conductorOpensTmpl         = mustTemplate("contracts/conductor-opens")
	digestTmpl                 = mustTemplate("briefings/digest")
	digestMessagesTmpl         = mustTemplate("briefings/digest-messages")
	digestNotesTmpl            = mustTemplate("briefings/digest-notes")
	digestPeersTmpl            = mustTemplate("briefings/digest-peers")
	epicTmpl                   = mustTemplate("briefings/epic")
	epicHeldTmpl               = mustTemplate("briefings/epic-held")
	epicNoteFromSessionTmpl    = mustTemplate("briefings/epic-note-from-session")
	findingAcceptedTmpl        = mustTemplate("briefings/finding-accepted")
	findingLineTmpl            = mustTemplate("briefings/finding-line")
	findingSettledTmpl         = mustTemplate("briefings/finding-settled")
	lostConversationReasonTmpl = mustTemplate("briefings/lost-conversation-reason")
	questionWithdrawnTmpl      = mustTemplate("briefings/question-withdrawn")
	rebriefSessionTmpl         = mustTemplate("briefings/rebrief-session")
	runLineTmpl                = mustTemplate("briefings/run-line")
	sessionTmpl                = mustTemplate("briefings/session")
	sessionFirstMessageTmpl    = mustTemplate("briefings/session-first-message")
	adoptedPrTmpl              = mustTemplate("contracts/adopted-pr")
	baseTmpl                   = mustTemplate("contracts/base")
	brainstormCheckoutsTmpl    = mustTemplate("contracts/brainstorm-checkouts")
	brainstormNotesTmpl        = mustTemplate("contracts/brainstorm-notes")
	conductorCheckoutTmpl      = mustTemplate("contracts/conductor-checkout")
	continueTmpl               = mustTemplate("contracts/continue")
	earlierFindingsTmpl        = mustTemplate("contracts/earlier-findings")
	feedbackTmpl               = mustTemplate("contracts/feedback")
	fromConductorTmpl          = mustTemplate("contracts/from-conductor")
	howYouWorkTmpl             = mustTemplate("contracts/how-you-work")
	machineTmpl                = mustTemplate("contracts/machine")
	machineNothingTmpl         = mustTemplate("contracts/machine-nothing")
	machineOfferTmpl           = mustTemplate("contracts/machine-offer")
	prFeedbackTmpl             = mustTemplate("contracts/pr-feedback")
	prFeedbackBotsTmpl         = mustTemplate("contracts/pr-feedback-bots")
	projectNotesTmpl           = mustTemplate("contracts/project-notes")
	restartedTmpl              = mustTemplate("contracts/restarted")
	reviewTmpl                 = mustTemplate("contracts/review")
	reviewFindingsTmpl         = mustTemplate("contracts/review-findings")
	reviewLeadTmpl             = mustTemplate("contracts/review-lead")
	severityTmpl               = mustTemplate("contracts/severity")
	taskTmpl                   = mustTemplate("contracts/task")
	taskForContextTmpl         = mustTemplate("contracts/task-for-context")
	taskImplementedTmpl        = mustTemplate("contracts/task-implemented")
	taskUnderReviewTmpl        = mustTemplate("contracts/task-under-review")
	workspaceTmpl              = mustTemplate("contracts/workspace")
)

// mustTemplate is the named template, or a panic naming its file.
func mustTemplate(name string) *template.Template {
	t := agentTemplates.Lookup(name)
	if t == nil {
		panic(fmt.Sprintf("agents/%s.md is missing", name))
	}
	return t
}

// mustLoad is v, or a panic with err: for what the package loads at init.
func mustLoad[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// agentFuncs are the template functions: formatting of data, never words.
var agentFuncs = template.FuncMap{
	"oneLine":  oneLine,
	"quote":    quote,
	"clip":     clip,
	"criteria": CriteriaList,
	"add1":     func(i int) int { return i + 1 },
	"short":    short,
	"join":     strings.Join,
	// quoteLines continues a Markdown quote over the text's lines.
	"quoteLines": func(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n> ") },
	// fenced breaks each ``` in a log so it cannot close the fence around it.
	"fenced": func(s string) string { return strings.ReplaceAll(s, "```", "`\u200b``") },
}

// fileText is a file's text: its content less one trailing newline.
func fileText(fsys fs.FS, name string) (string, error) {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return "", err
	}
	text := strings.TrimSuffix(string(b), "\n")
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s is empty", name)
	}
	return text, nil
}

func parseAgentTemplates(fsys fs.FS) (*template.Template, error) {
	root := template.New("").Option("missingkey=error").Funcs(agentFuncs)
	err := fs.WalkDir(fsys, "agents", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".md" || strings.HasPrefix(p, "agents/roles/") {
			return err
		}
		text, err := fileText(fsys, p)
		if err != nil {
			return err
		}
		if _, err := root.New(agentTemplateName(p)).Parse(text); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// text/template resolves {{template "x"}} only when it executes.
	for _, t := range root.Templates() {
		if t.Tree == nil {
			continue
		}
		for _, name := range includedTemplates(t.Tree.Root) {
			if root.Lookup(name) == nil {
				return nil, fmt.Errorf("agents/%s.md includes agents/%s.md, which is missing", t.Name(), name)
			}
		}
	}
	return root, nil
}

// includedTemplates are the names of the {{template}} actions under n.
func includedTemplates(n parse.Node) []string {
	switch n := n.(type) {
	case *parse.TemplateNode:
		return []string{n.Name}
	case *parse.ListNode:
		if n == nil {
			return nil
		}
		var out []string
		for _, c := range n.Nodes {
			out = append(out, includedTemplates(c)...)
		}
		return out
	case *parse.IfNode:
		return append(includedTemplates(n.List), includedTemplates(n.ElseList)...)
	case *parse.RangeNode:
		return append(includedTemplates(n.List), includedTemplates(n.ElseList)...)
	case *parse.WithNode:
		return append(includedTemplates(n.List), includedTemplates(n.ElseList)...)
	}
	return nil
}

// agentTemplateName is a file's template name: "agents/contracts/commit.md"
// is "contracts/commit".
func agentTemplateName(file string) string {
	return strings.TrimSuffix(strings.TrimPrefix(file, "agents/"), ".md")
}

// render executes t, and panics on an execution error: a field or method
// the data's type lacks, or a function that fails. Data of the right type
// that is empty (a nil slice, a nil value) renders without error, so each
// caller guards that; the goldens pin what every template renders.
func render(t *template.Template, data any) string {
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		panic(fmt.Sprintf("agents/%s.md: %v", t.Name(), err))
	}
	return b.String()
}

// agentTexts renders each template under prefix ("contracts/review-focus/")
// once, keyed by the rest of its name. Each of want must be among them.
func agentTexts(prefix string, want ...string) map[string]string {
	return agentTextsIn(agentTemplates, prefix, want...)
}

// ToolText is agents/tools/<key>.md rendered with data: a tool's
// description ("list_tasks", "session/list_tasks"), or the tools' shared
// text ("common/server-instructions"). A missing file panics.
func ToolText(key string, data any) string {
	return render(mustTemplate("tools/"+key), data)
}

// ToolParameter is the description of the parameter at path ("questions",
// "questions.header" for a field of its items) of the tool whose text is
// key: the template "tools/<key>/<path>" its file defines. ok is false
// when the file defines none.
func ToolParameter(key, path string) (text string, ok bool) {
	t := agentTemplates.Lookup("tools/" + key + "/" + path)
	if t == nil {
		return "", false
	}
	return render(t, nil), true
}

// ToolParameters are the parameter paths the tool whose text is key has
// descriptions for.
func ToolParameters(key string) []string {
	var out []string
	for _, t := range agentTemplates.Templates() {
		if path, ok := strings.CutPrefix(t.Name(), "tools/"+key+"/"); ok {
			out = append(out, path)
		}
	}
	return out
}

func agentTextsIn(set *template.Template, prefix string, want ...string) map[string]string {
	out := map[string]string{}
	for _, t := range set.Templates() {
		if key, ok := strings.CutPrefix(t.Name(), prefix); ok {
			out[key] = render(t, nil)
		}
	}
	for _, key := range want {
		if _, ok := out[key]; !ok {
			panic(fmt.Sprintf("agents/%s%s.md is missing", prefix, key))
		}
	}
	return out
}

// loadRoles reads roles/<role>.md: a role's instructions, one paragraph
// per blank-line-separated block (instructions keeps the fixer's apart).
func loadRoles(fsys fs.FS) (map[string][]string, error) {
	files, err := fs.Glob(fsys, "agents/roles/*.md")
	if err != nil {
		return nil, err
	}
	roles := map[string][]string{}
	for _, f := range files {
		text, err := fileText(fsys, f)
		if err != nil {
			return nil, err
		}
		roles[strings.TrimSuffix(path.Base(f), ".md")] = strings.Split(text, "\n\n")
	}
	return roles, nil
}

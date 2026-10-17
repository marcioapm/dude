package delivery

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"text/template"
)

// What dude tells agents lives in Markdown under agents/ (its README says
// how to edit it). roles/ holds each role's replaceable instructions as
// plain text; contracts/ and briefings/ are text/template files, each
// named by its path without ".md" ("contracts/commit"). A file's one
// trailing newline is not part of its text. Every file is read and parsed
// when the package initialises: one that is missing or fails to parse
// stops the process at startup, never renders empty.
//
//go:embed agents/roles/*.md
var agentFiles embed.FS

var agentTemplates = mustParse(parseAgentTemplates(agentFiles))

func mustParse(t *template.Template, err error) *template.Template {
	if err != nil {
		panic(err)
	}
	return t
}

// agentFuncs are the template functions: formatting of data, never words.
var agentFuncs = template.FuncMap{
	"oneLine":  oneLine,
	"quote":    quote,
	"clip":     clip,
	"criteria": CriteriaList,
	"add1":     func(i int) int { return i + 1 },
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
	return root, err
}

// agentTemplateName is a file's template name: "agents/contracts/commit.md"
// is "contracts/commit".
func agentTemplateName(file string) string {
	return strings.TrimSuffix(strings.TrimPrefix(file, "agents/"), ".md")
}

// render executes the named template. An error is a template that does not
// fit the data it is given, a defect the golden tests catch: it panics
// rather than send an agent a prompt with a section missing.
func render(name string, data any) string {
	var b strings.Builder
	if err := agentTemplates.ExecuteTemplate(&b, name, data); err != nil {
		panic(fmt.Sprintf("agents/%s.md: %v", name, err))
	}
	return b.String()
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

func mustLoadRoles(roles map[string][]string, err error) map[string][]string {
	if err != nil {
		panic(err)
	}
	return roles
}

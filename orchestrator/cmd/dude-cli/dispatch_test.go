package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// runPlain runs args with a fake server and returns stdout, the error and
// how many requests reached the server.
func runPlain(t *testing.T, args ...string) (string, error, int) {
	t.Helper()
	srv, calls := fakeDude(t)
	out, err := runCLI(t, srv, "", args...)
	return out, err, len(*calls)
}

func TestHelpListsTopLevelCommandsAndNouns(t *testing.T) {
	for _, args := range [][]string{{}, {"--help"}, {"help"}, {"-h"}} {
		out, err, n := runPlain(t, args...)
		if err != nil || n != 0 {
			t.Fatalf("%v: %v, %d requests", args, err, n)
		}
		for _, want := range []string{"login URL", "logout", "orgs", "use ORG", "whoami", "upgrade", "guide", "api METHOD PATH",
			"dude NOUN for its commands"} {
			if !strings.Contains(out, want) {
				t.Errorf("%v: no %q in\n%s", args, want, out)
			}
		}
		for _, n := range nouns {
			if !strings.Contains(out, "\n  "+n.name+" ") {
				t.Errorf("%v: noun %q not listed", args, n.name)
			}
		}
		if strings.Contains(out, "automation update") || strings.Contains(out, "task update") {
			t.Errorf("%v: dude --help lists verbs:\n%s", args, out)
		}
	}
}

// Every noun dude --help lists has commands, and every command's noun is listed.
func TestNounsMatchTheCommandTable(t *testing.T) {
	listed := map[string]bool{}
	for _, n := range nouns {
		listed[n.name] = true
		if len(verbs(n.name)) == 0 {
			t.Errorf("noun %q has no commands", n.name)
		}
	}
	for name := range commands {
		if noun, _, ok := strings.Cut(name, " "); ok && !listed[noun] {
			t.Errorf("%q: noun %q is not in nouns", name, noun)
		}
	}
}

func TestNounAloneListsItsVerbs(t *testing.T) {
	for _, args := range [][]string{{"task"}, {"task", "--help"}, {"task", "help"}, {"--org", "x", "task", "-h"},
		{"help", "task"}, {"--help", "task"}} {
		out, err, n := runPlain(t, args...)
		if err != nil || n != 0 {
			t.Fatalf("%v: %v, %d requests", args, err, n)
		}
		if !strings.HasPrefix(out, "usage: dude task VERB") {
			t.Fatalf("%v:\n%s", args, out)
		}
		// Sorted, each with its arguments and help.
		at := 0
		for _, v := range verbs("task") {
			line := "\n  " + strings.TrimSpace(v+" "+commands["task "+v].args)
			i := strings.Index(out[at:], line)
			if i < 0 {
				t.Fatalf("%v: %q missing or out of order in\n%s", args, line, out)
			}
			at += i + len(line)
			if !strings.Contains(out, commands["task "+v].help) {
				t.Errorf("%v: no help for %s", args, v)
			}
		}
	}
}

// dude help NOUN is dude NOUN help; help with a word that is no noun is dude --help.
func TestHelpWithANoun(t *testing.T) {
	want, _, _ := runPlain(t, "review-question", "help")
	got, err, n := runPlain(t, "help", "review-question")
	if err != nil || n != 0 || got != want || !strings.HasPrefix(got, "usage: dude review-question VERB") {
		t.Fatalf("help review-question: %v, %d requests\n%s", err, n, got)
	}
	top, err, n := runPlain(t, "help", "frobnicate")
	if err != nil || n != 0 || top != usage() {
		t.Fatalf("help frobnicate: %v, %d requests\n%s", err, n, top)
	}
}

func TestUnknownNounAndVerb(t *testing.T) {
	_, err, n := runPlain(t, "frobnicate", "x")
	if err == nil || err.Error() != `unknown command "frobnicate"; see dude --help` || n != 0 {
		t.Fatalf("unknown noun: %v, %d requests", err, n)
	}
	_, err, n = runPlain(t, "task", "frob", "wi_12")
	if err == nil || n != 0 || !strings.HasPrefix(err.Error(), `dude task has no "frob"; its commands: `) {
		t.Fatalf("unknown verb: %v, %d requests", err, n)
	}
	if !strings.Contains(err.Error(), strings.Join(verbs("task"), ", ")) {
		t.Fatalf("unknown verb lists %q", err)
	}
}

func TestVerbHelpNamesTheFullCommand(t *testing.T) {
	out, err, n := runPlain(t, "task", "update", "--help")
	if err != nil || n != 0 || !strings.HasPrefix(out, "usage: dude task update TASK\n") || !strings.Contains(out, "-no-criteria") {
		t.Fatalf("%v %d\n%s", err, n, out)
	}
	_, err, _ = runPlain(t, "automation", "update", "aut_1")
	if err == nil || err.Error() != "usage: dude automation update ID SETTINGS" {
		t.Fatalf("argument count: %v", err)
	}
	// The flag set is named for the full command too: its errors say so.
	var out2 bytes.Buffer
	c := &cli{out: &out2}
	if err := c.run([]string{"task", "update", "wi_12", "--nope"}); err == nil || !strings.Contains(out2.String(), "usage: dude task update") {
		t.Fatalf("bad flag: %v\n%s", err, out2.String())
	}
}

func TestOrgBeforeANoun(t *testing.T) {
	srv, calls := fakeDude(t)
	var asked string
	c := &cli{http: srv.Client(), stdin: strings.NewReader(""), out: io.Discard,
		creds: func(org string) (string, string, error) { asked = org; return srv.URL, "k", nil }}
	if err := c.run([]string{"--org", "x", "task", "list", "ABS"}); err != nil {
		t.Fatal(err)
	}
	if asked != "x" || last(*calls).path != "/v1/tasks?projectId=prj_1" {
		t.Fatalf("org %q, calls %+v", asked, *calls)
	}
}

// The flat names are gone, with no alias: each is an unknown command and
// sends nothing.
func TestOldFlatNamesAreUnknown(t *testing.T) {
	for _, args := range [][]string{
		{"automation-update", "aut_1", "-"},
		{"show", "wi_12"},
		{"prs", "wi_12"},
		{"list", "ABS"},
		{"automations"},
		{"brainstorms"},
		{"memories"},
		{"people"},
		{"epic-create", "ABS", "--title", "x"},
	} {
		_, err, n := runPlain(t, args...)
		if err == nil || err.Error() != `unknown command "`+args[0]+`"; see dude --help` || n != 0 {
			t.Errorf("%v: %v, %d requests", args, err, n)
		}
	}
}

func TestVerbHelpIsNotAnError(t *testing.T) {
	out, err, n := runPlain(t, "task", "show", "-h")
	if err != nil || n != 0 || !strings.HasPrefix(out, "usage: dude task show TASK\n") {
		t.Fatalf("task show -h: %v, %d requests\n%s", err, n, out)
	}
}

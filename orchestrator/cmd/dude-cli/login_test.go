package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

type fakeTerminal struct {
	tty   bool
	typed string
	reads int
}

func (f *fakeTerminal) isTerminal() bool { return f.tty }
func (f *fakeTerminal) readPassword() ([]byte, error) {
	f.reads++
	return []byte(f.typed), nil
}

// unread fails the test when the key is read from stdin.
type unread struct{ t *testing.T }

func (u unread) Read([]byte) (int, error) {
	u.t.Error("read stdin on a terminal")
	return 0, errors.New("unread")
}

func TestLoginPipedKeyIsReadAsBefore(t *testing.T) {
	store := isolate(t, false)
	srv, calls := fakeDude(t)
	tty := &fakeTerminal{}
	var out, errOut bytes.Buffer
	c := &cli{login: login, http: srv.Client(), stdin: strings.NewReader("piped-key\nrest"), out: &out, errOut: &errOut, term: tty}
	if err := c.run([]string{"login", srv.URL}); err != nil {
		t.Fatal(err)
	}
	if tty.reads != 0 || last(*calls).auth != "Bearer piped-key" || store.keys[keyID("acme", srv.URL)] != "piped-key" {
		t.Fatalf("reads %d, calls %+v, store %v", tty.reads, *calls, store.keys)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr: %q", errOut.String())
	}
}

func TestLoginOnATerminalReadsWithoutEcho(t *testing.T) {
	store := isolate(t, false)
	srv, calls := fakeDude(t)
	tty := &fakeTerminal{tty: true, typed: " typed-key "}
	var out, errOut bytes.Buffer
	c := &cli{login: login, http: srv.Client(), stdin: unread{t}, out: &out, errOut: &errOut, term: tty}
	if err := c.run([]string{"login", srv.URL}); err != nil {
		t.Fatal(err)
	}
	if tty.reads != 1 || last(*calls).auth != "Bearer typed-key" || store.keys[keyID("acme", srv.URL)] != "typed-key" {
		t.Fatalf("reads %d, calls %+v, store %v", tty.reads, *calls, store.keys)
	}
	if want := "Key for " + srv.URL + " (dude: Your settings → Keys): \n"; errOut.String() != want {
		t.Fatalf("stderr %q, want %q", errOut.String(), want)
	}
	if strings.Contains(out.String()+errOut.String(), "typed-key") || strings.Contains(out.String(), "Key for") {
		t.Fatalf("stdout %q, stderr %q", out.String(), errOut.String())
	}
}

func TestInterruptAtThePromptRestoresTheTerminal(t *testing.T) {
	sigs := make(chan os.Signal, 1)
	done := make(chan struct{})
	var restored, exited bool
	code := -1
	var w bytes.Buffer
	sigs <- os.Interrupt
	watchInterrupt(sigs, done, func() error {
		if exited || w.Len() != 0 {
			t.Error("restored after the newline or the exit")
		}
		restored = true
		return nil
	}, &w, func(c int) { exited, code = true, c })
	if !restored || w.String() != "\n" || code != 130 {
		t.Fatalf("restored %v, wrote %q, exit %d", restored, w.String(), code)
	}

	// After the read, a closed done ends the watch without touching anything.
	close(done)
	watchInterrupt(make(chan os.Signal), done, func() error { t.Error("restored"); return nil }, &w, func(int) { t.Error("exited") })
}

func TestLoginOnATerminalRefusesAnEmptyKey(t *testing.T) {
	isolate(t, false)
	srv, calls := fakeDude(t)
	var errOut bytes.Buffer
	c := &cli{login: login, http: srv.Client(), stdin: unread{t}, out: &bytes.Buffer{}, errOut: &errOut, term: &fakeTerminal{tty: true, typed: "  "}}
	if err := c.run([]string{"login", srv.URL}); err == nil || !strings.Contains(err.Error(), "empty") || len(*calls) != 0 {
		t.Fatalf("%v %+v", err, *calls)
	}
}

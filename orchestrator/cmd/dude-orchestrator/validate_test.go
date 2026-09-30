package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/config"
)

// TestValidateHelper is the orchestrator's main for TestValidate, with the
// arguments in DUDE_TEST_VALIDATE_ARGS; alone it does nothing.
func TestValidateHelper(t *testing.T) {
	if os.Getenv("DUDE_TEST_VALIDATE_HELPER") != "1" {
		t.Skip("started by TestValidate")
	}
	os.Args = append([]string{"dude-orchestrator"}, strings.Fields(os.Getenv("DUDE_TEST_VALIDATE_ARGS"))...)
	main()
	os.Exit(0)
}

// listener counts connections to an address the test owns.
func listener(t *testing.T) (addr string, connections func() int64) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	var n atomic.Int64
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			c.Close()
		}
	}()
	return l.Addr().String(), n.Load
}

type validateRun struct {
	exit           int
	stdout, stderr string
}

func runValidate(t *testing.T, text string, mode os.FileMode, args ...string) (validateRun, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dude.toml")
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestValidateHelper$", "-test.count=1")
	// Only the file configures it: none of this machine's own settings.
	cmd.Env = []string{"DUDE_TEST_VALIDATE_HELPER=1", "DUDE_TEST_VALIDATE_ARGS=" + strings.Join(append([]string{"validate"}, args...), " "),
		"DUDE_CONFIG=" + path, "HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("validate did not exit: %s", stderr.String())
	}
	r := validateRun{stdout: stdout.String(), stderr: stderr.String()}
	if exit, ok := err.(*exec.ExitError); ok {
		r.exit = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return r, path
}

// startupError is the error startup's resolve gives for the file at path.
func startupError(t *testing.T, path string) string {
	t.Helper()
	_, _, err := resolve(config.Options{Getenv: func(k string) string {
		if k == "DUDE_CONFIG" {
			return path
		}
		return ""
	}, DefaultPath: filepath.Join(t.TempDir(), "absent.toml")})
	if err == nil {
		t.Fatal("startup accepts the file")
	}
	return err.Error()
}

func TestValidate(t *testing.T) {
	const sentinel = "S3NT1NEL-validate-lux-key-7c1e"
	addr, connections := listener(t)
	// Every address in the file is the test's own listener: a connection to
	// any of them is one validate must not make.
	good := `[database]
url = "postgres://dude:pw@` + addr + `/dude?connect_timeout=2"
[orchestrator]
token = "file-token"
listen = "` + addr + `"
[lux]
url = "http://` + addr + `"
api_key = "` + sentinel + `"
[llm]
url = "http://` + addr + `/v1"
key = "llm-key"
[tools]
listen = "` + addr + `"
url = "http://` + addr + `"
`
	check := func(name string, r validateRun, exit int, stdout string, stderrHas ...string) {
		t.Helper()
		if r.exit != exit || r.stdout != stdout {
			t.Errorf("%s: exit %d stdout %q, want %d %q; stderr %q", name, r.exit, r.stdout, exit, stdout, r.stderr)
		}
		for _, s := range stderrHas {
			if !strings.Contains(r.stderr, s) {
				t.Errorf("%s: stderr %q, want %q in it", name, r.stderr, s)
			}
		}
		if strings.Contains(r.stdout+r.stderr, sentinel) {
			t.Errorf("%s: the secret was printed: %q %q", name, r.stdout, r.stderr)
		}
	}

	r, path := runValidate(t, good, 0o600)
	check("valid", r, 0, "ok: "+path+"\n")
	if r.stderr != "" {
		t.Errorf("valid: stderr %q, want nothing", r.stderr)
	}

	r, path = runValidate(t, good+"[agent]\nimgae = \"x\"\n", 0o600)
	check("unknown key", r, 1, "", "unknown key agent.imgae")
	if want := startupError(t, path) + "\n"; r.stderr != want {
		t.Errorf("unknown key: stderr %q, want startup's %q", r.stderr, want)
	}

	r, _ = runValidate(t, strings.Replace(good, `api_key = "`+sentinel+`"`, "", 1), 0o600)
	check("missing", r, 1, "")
	if r.stderr != "lux.api_key (LUX_API_KEY) is required\n" {
		t.Errorf("missing: stderr %q", r.stderr)
	}

	r, path = runValidate(t, good+"[registry]\nauth = \"ecr\"\n", 0o600)
	check("settings", r, 1, "", "needs DUDE_AGENT_IMAGE in an ECR registry")
	if want := startupError(t, path) + "\n"; r.stderr != want {
		t.Errorf("settings: stderr %q, want startup's %q", r.stderr, want)
	}

	r, path = runValidate(t, good, 0o644)
	check("world-readable secret", r, 0, "ok: "+path+"\n", "warning: ", "lux.api_key", "mode 0644")
	if n := strings.Count(r.stderr, "\n"); n != 1 {
		t.Errorf("world-readable secret: %d stderr lines, want 1: %q", n, r.stderr)
	}

	r, _ = runValidate(t, good, 0o600, "extra")
	check("extra argument", r, 2, "", "usage: dude-orchestrator validate")

	if n := connections(); n != 0 {
		t.Errorf("validate made %d connections", n)
	}
}

func TestValidateWithNoFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	vars := map[string]string{"DATABASE_URL": "postgres://x/y", "DUDE_ORCHESTRATOR_TOKEN": "t",
		"LUX_URL": "https://lux.example", "LUX_API_KEY": "k"}
	exit := validate(nil, config.Options{Getenv: func(k string) string { return vars[k] },
		DefaultPath: filepath.Join(t.TempDir(), "absent.toml")}, &stdout, &stderr)
	if exit != 0 || stdout.String() != "ok: no file\n" || stderr.String() != "" {
		t.Errorf("exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
	}
}

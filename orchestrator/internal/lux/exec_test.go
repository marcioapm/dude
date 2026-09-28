package lux_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A stand-in for luxd's exec relay, speaking its protocol: the command
// first, then output as base64 data on a channel, then the exit code.
func execServer(t *testing.T, handle func(ctx context.Context, ws *websocket.Conn, command []string)) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, `{"error":{"code":"unauthorized","message":"no"}}`, 401)
			return
		}
		if strings.Contains(r.URL.Path, "stopped") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":{"code":"not_running","message":"run is stopped: interactive access needs it running"}}`))
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		var open struct {
			Command []string `json:"command"`
		}
		if err := wsjson.Read(r.Context(), ws, &open); err != nil {
			return
		}
		handle(r.Context(), ws, open.Command)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestExecReturnsWhatTheCommandPrintedAndItsExitCode(t *testing.T) {
	url := execServer(t, func(ctx context.Context, ws *websocket.Conn, command []string) {
		// Input is closed at once: the command gets no stdin.
		var eof struct {
			EOF bool `json:"eof"`
		}
		_ = wsjson.Read(ctx, ws, &eof)
		if !eof.EOF || strings.Join(command, " ") != "git diff" {
			_ = wsjson.Write(ctx, ws, map[string]any{"error": "unexpected"})
			return
		}
		_ = wsjson.Write(ctx, ws, map[string]any{"data": []byte("diff "), "ch": "stdout"})
		_ = wsjson.Write(ctx, ws, map[string]any{"data": []byte("warning\n"), "ch": "stderr"})
		_ = wsjson.Write(ctx, ws, map[string]any{"data": []byte("--git\n"), "ch": "stdout"})
		_ = wsjson.Write(ctx, ws, map[string]any{"exitCode": 2})
		_ = ws.Close(websocket.StatusNormalClosure, "")
	})
	res, err := lux.New(url, "k").Exec(context.Background(), "run_1", []string{"git", "diff"})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Stdout) != "diff --git\n" || string(res.Stderr) != "warning\n" || res.ExitCode != 2 {
		t.Errorf("got %q %q %d", res.Stdout, res.Stderr, res.ExitCode)
	}
}

func TestExecRefusedBeforeTheUpgradeIsLuxsError(t *testing.T) {
	url := execServer(t, nil)
	_, err := lux.New(url, "k").Exec(context.Background(), "stopped", []string{"true"})
	e, ok := lux.AsError(err)
	if !ok || e.Status != 409 || e.Code != "not_running" {
		t.Errorf("got %v", err)
	}
}

func TestAStreamThatEndsWithAnErrorIsAnError(t *testing.T) {
	url := execServer(t, func(ctx context.Context, ws *websocket.Conn, _ []string) {
		_ = wsjson.Write(ctx, ws, map[string]any{"error": "the Run's host disconnected"})
		_ = ws.Close(websocket.StatusNormalClosure, "")
	})
	_, err := lux.New(url, "k").Exec(context.Background(), "run_1", []string{"true"})
	if err == nil || !strings.Contains(err.Error(), "host disconnected") {
		t.Errorf("got %v", err)
	}
}

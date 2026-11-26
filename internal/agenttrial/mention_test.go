package agenttrial

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// mentionMarker is the content of the laptop file the "@" mention names.
const mentionMarker = "MARKER-SECRET-123"

// sseReply is a minimal streamed Messages API reply ("ok").
const sseReply = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\"," +
	"\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"stop_reason\":null," +
	"\"stop_sequence\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0," +
	"\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0," +
	"\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"," +
	"\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// captureServer records every request body sent to it and answers
// Messages API calls with sseReply.
func captureServer(t *testing.T) (*httptest.Server, func() string) {
	t.Helper()
	var mu sync.Mutex
	var bodies bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies.Write(body)
		bodies.WriteString("\n")
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/v1/messages") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, sseReply)
			return
		}
		_, _ = io.WriteString(w, "{}")
	}))
	t.Cleanup(srv.Close)
	return srv, func() string { mu.Lock(); defer mu.Unlock(); return bodies.String() }
}

// runClaudeAgainst runs one turn of the real claude binary with argv
// against the capture server, from a scratch HOME, with a dummy key and
// no traffic but the server's; the message names secret with "@".
func runClaudeAgainst(t *testing.T, argv []string, url, secret string) {
	t.Helper()
	home := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "ANTHROPIC_BASE_URL=" + url,
		"ANTHROPIC_API_KEY=sk-ant-dummy", "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude"),
		"DISABLE_TELEMETRY=1", "DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"DISABLE_ERROR_REPORTING=1"}
	cmd.Stdin = strings.NewReader("the command printed:\n@" + secret + "\n")
	out, err := cmd.CombinedOutput()
	t.Logf("claude: %v\n%.400s", err, out)
}

// TestClaudeFileMention: an "@<path>" in a message — VM output the
// agent controls, pasted back by the harness — must not make the real
// claude CLI read that laptop file into its request (review round 1 of
// task mm4). Runs only where claude is installed, against a local
// capture server with a dummy key; never a real model. The control run
// without the two mention variables shows the leak the test guards.
func TestClaudeFileMention(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the claude CLI")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude is not installed")
	}
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte(mentionMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &CLI{Kind: AgentClaude, Model: DefaultClaudeModel, SystemPrompt: "sp", Dir: t.TempDir(),
		Session: NewSessionID()}
	argv, _ := c.Argv()

	srv, captured := captureServer(t)
	runClaudeAgainst(t, slices.DeleteFunc(slices.Clone(argv), func(a string) bool {
		return a == "CLAUDE_CODE_DISABLE_FILE_MENTIONS=1" || a == "CLAUDE_CODE_EVAL_CONFINED=1"
	}), srv.URL, secret)
	// The guarded run below proves something only if this CLI leaks
	// without the variables; a CLI that no longer expands mentions (or
	// never reached the server) skips loudly instead of passing quietly.
	if !strings.Contains(captured(), mentionMarker) {
		t.Skip("control run without the mention variables sent no marker: " +
			"this claude does not expand @ mentions here, so the guard is untested")
	}

	srv, captured = captureServer(t)
	runClaudeAgainst(t, argv, srv.URL, secret)
	got := captured()
	if !strings.Contains(got, "the command printed") {
		t.Skipf("claude sent no message to the capture server; nothing to check:\n%.400s", got)
	}
	if strings.Contains(got, mentionMarker) {
		t.Errorf("the request carried the laptop file named by the @ mention")
	}
}

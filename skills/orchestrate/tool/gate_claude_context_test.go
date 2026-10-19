package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInstalledClaudeContextFilesToggle(t *testing.T) {
	if os.Getenv("ORCH_TEST_RUNNER_CLAUDE") != "1" {
		t.Skip("set ORCH_TEST_RUNNER_CLAUDE=1 for installed Claude loopback verification")
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip(err)
	}
	for _, on := range []bool{false, true} {
		t.Run(onOff(on), func(t *testing.T) {
			repo, home := t.TempDir(), t.TempDir()
			const marker = "ROUND_TWO_CONTEXT_SENTINEL_91240"
			os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("Project note: "+marker+"\n"), 0o644)
			var mu sync.Mutex
			seen := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]any
				json.NewDecoder(r.Body).Decode(&req)
				raw, _ := json.Marshal(req)
				mu.Lock()
				seen = seen || strings.Contains(string(raw), marker)
				mu.Unlock()
				if strings.Contains(r.URL.Path, "count_tokens") {
					fmt.Fprint(w, `{"input_tokens":1}`)
					return
				}
				content := []any{map[string]string{"type": "text", "text": "context mock complete"}}
				message := map[string]any{"id": "msg_ctx_toggle", "type": "message", "role": "assistant", "model": "mock", "content": content, "stop_reason": "end_turn", "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}}
				if stream, _ := req["stream"].(bool); !stream {
					json.NewEncoder(w).Encode(message)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				send := func(event string, value any) {
					b, _ := json.Marshal(value)
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
				}
				message["content"] = []any{}
				message["stop_reason"] = nil
				send("message_start", map[string]any{"type": "message_start", "message": message})
				send("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}})
				send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": "context mock complete"}})
				send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
				send("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 1}})
				send("message_stop", map[string]string{"type": "message_stop"})
			}))
			defer srv.Close()
			for _, value := range os.Environ() {
				key := strings.SplitN(value, "=", 2)[0]
				if strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "CLAUDE_") || key == "CLAUDECODE" {
					t.Setenv(key, "")
				}
			}
			t.Setenv("HOME", home)
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
			t.Setenv("ORCH_CLAUDE", bin)
			t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
			t.Setenv("ANTHROPIC_API_KEY", "mock")
			t.Setenv("ANTHROPIC_AUTH_TOKEN", "mock")
			t.Setenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "1")
			t.Setenv("DISABLE_AUTOUPDATER", "1")
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			p := gatePass{prompt: "Say context mock complete, then stop.", contextFiles: on, ctx: ctx}
			out, err := runClaude(repo, profile{Harness: "claude", Model: "haiku", Effort: "low"}, p, filepath.Join(home, "session"))
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			t.Logf("context-files=%s CLAUDE.md marker sent=%v output=%s", onOff(on), seen, strings.TrimSpace(out))
			if seen != on {
				t.Fatalf("context flag did not control CLAUDE.md discovery")
			}
		})
	}
}

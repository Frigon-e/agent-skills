package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// A pane pass runs pi as an interactive agent; its reply is read from the
// session file pi saves, whose lines are events of this shape.

type piEvent struct {
	Type    string `json:"type"`
	Message struct {
		Role     string          `json:"role"`
		ToolName string          `json:"toolName"`
		Content  json.RawMessage `json:"content"`
		Usage    struct {
			Input     int `json:"input"`
			Output    int `json:"output"`
			CacheRead int `json:"cacheRead"`
		} `json:"usage"`
	} `json:"message"`
}

// text joins the text parts of an assistant message; other roles carry a
// string or other shapes and give "".
func (e *piEvent) text() string {
	if e.Message.Role != "assistant" {
		return ""
	}
	var parts []struct{ Type, Text string }
	if json.Unmarshal(e.Message.Content, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// eachPiEvent calls fn for every well-formed JSONL line of r; other lines are
// skipped. Lines can be hundreds of KB, so it does not use a bounded scanner.
func eachPiEvent(r io.Reader, fn func(*piEvent)) {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			var e piEvent
			if json.Unmarshal(line, &e) == nil && e.Type != "" {
				fn(&e)
			}
		}
		if err != nil {
			return
		}
	}
}

// lastReply is the text of the last assistant message in a pi session; a pass whose last assistant message has no text has no reply.
func lastReply(r io.Reader) (string, error) {
	text, seen := "", false
	eachPiEvent(r, func(e *piEvent) {
		if (e.Type == "message" || e.Type == "message_end") && e.Message.Role == "assistant" {
			text, seen = e.text(), true
		}
	})
	if !seen || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("pi gave no reply")
	}
	return text, nil
}

package messaging

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelegramBackend_PostsJSONToBotEndpoint(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tb := &TelegramBackend{
		NameStr: "tg", Token: "123:abc", ChatID: "9999",
		EndpointOverride: srv.URL,
	}
	err := tb.Send(context.Background(), Message{
		Title: "Chief · alpha — Attention needed",
		Body:  "Which auth strategy?",
		URL:   "chief://flag/abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/bot123:abc/sendMessage" {
		t.Errorf("path: got %q", gotPath)
	}
	if gotBody["chat_id"] != "9999" {
		t.Errorf("chat_id: got %v", gotBody["chat_id"])
	}
	txt, _ := gotBody["text"].(string)
	if !strings.Contains(txt, "Which auth strategy") {
		t.Errorf("text missing body: %q", txt)
	}
	if !strings.Contains(txt, "chief://flag/abc") {
		t.Errorf("text missing URL link: %q", txt)
	}
	if !strings.Contains(txt, `\_`) && !strings.Contains(txt, "*") {
		// Title should have gotten wrapped in *…* markdown.
		if !strings.Contains(txt, "Attention needed") {
			t.Errorf("text missing title fragment: %q", txt)
		}
	}
}

func TestTelegramBackend_RequiresTokenAndChatID(t *testing.T) {
	err := (&TelegramBackend{NameStr: "t"}).Send(context.Background(), Message{Body: "x"})
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("want required error, got %v", err)
	}
}

func TestEscapeMarkdown(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hello", "hello"},
		{"*bold*", `\*bold\*`},
		{"_italic_", `\_italic\_`},
		{"[link](url)", `\[link](url)`},
		{"`code`", "\\`code\\`"},
	}
	for _, c := range cases {
		if got := escapeMarkdown(c.in); got != c.want {
			t.Errorf("escapeMarkdown(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestSlackBackend_PostsBlocksWithFallback(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	sb := &SlackBackend{NameStr: "s", Webhook: srv.URL}
	err := sb.Send(context.Background(), Message{
		Title:       "Chief · alpha",
		Body:        "Something's up",
		URL:         "chief://x",
		ProjectName: "alpha",
		Urgency:     UrgencyUrgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	fallback, _ := gotBody["text"].(string)
	if !strings.Contains(fallback, "Chief · alpha") {
		t.Errorf("fallback text: got %q", fallback)
	}
	blocks, _ := gotBody["blocks"].([]any)
	if len(blocks) < 2 {
		t.Fatalf("want >=2 blocks (header + section), got %d", len(blocks))
	}
	if blocks[0].(map[string]any)["type"] != "header" {
		t.Errorf("first block should be header")
	}
}

func TestSlackBackend_WebhookRequired(t *testing.T) {
	err := (&SlackBackend{NameStr: "s"}).Send(context.Background(), Message{Body: "x"})
	if err == nil || !strings.Contains(err.Error(), "webhook required") {
		t.Errorf("want webhook-required error, got %v", err)
	}
}

func TestSlackBackend_PropagatesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte("invalid_payload"))
	}))
	defer srv.Close()
	sb := &SlackBackend{NameStr: "s", Webhook: srv.URL}
	err := sb.Send(context.Background(), Message{Body: "x"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Errorf("want HTTP 400 error, got %v", err)
	}
}

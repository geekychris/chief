package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// TelegramBackend delivers messages via a Telegram bot's sendMessage
// API. Outbound-only in this commit; inbound (bot command parsing +
// long-poll loop) lands as a follow-up along with the IncomingBackend
// wire-up in chiefd.
//
// Config:
//
//	messaging:
//	  backends:
//	    - name: telegram
//	      type: telegram
//	      token:   <BotFather-issued token>
//	      chat_id: <destination chat or channel id>
//
// Get a token via BotFather (t.me/BotFather → /newbot). Get the chat_id
// by sending the bot a message, then hitting
// https://api.telegram.org/bot<token>/getUpdates and reading
// message.chat.id from the response.
type TelegramBackend struct {
	NameStr string
	Token   string
	ChatID  string
	HTTP    *http.Client

	// EndpointOverride is a test seam. Empty defaults to the real
	// Telegram Bot API host.
	EndpointOverride string
}

// Name reports this backend's identifier.
func (t *TelegramBackend) Name() string { return t.NameStr }

// Send POSTs a JSON body to <api>/bot<token>/sendMessage. Body is
// composed as "*Title*\n<body>" with Markdown so the title stands
// out in the delivered message.
func (t *TelegramBackend) Send(ctx context.Context, m Message) error {
	if t.Token == "" || t.ChatID == "" {
		return fmt.Errorf("telegram backend %q: token and chat_id required", t.NameStr)
	}
	base := t.EndpointOverride
	if base == "" {
		base = "https://api.telegram.org"
	}
	url := fmt.Sprintf("%s/bot%s/sendMessage", strings.TrimRight(base, "/"), t.Token)

	// Compose text with the urgency + optional deep-link. Telegram
	// truncates messages > 4096 chars — chief bodies rarely reach that,
	// but clamp defensively.
	var b strings.Builder
	if m.Title != "" {
		fmt.Fprintf(&b, "*%s*\n", escapeMarkdown(m.Title))
	}
	if m.Body != "" {
		b.WriteString(escapeMarkdown(m.Body))
	}
	if m.URL != "" {
		fmt.Fprintf(&b, "\n[%s](%s)", "open in chief", m.URL)
	}
	text := b.String()
	if len(text) > 4096 {
		text = text[:4090] + "…"
	}

	payload, _ := json.Marshal(map[string]any{
		"chat_id":                  t.ChatID,
		"text":                     text,
		"parse_mode":               "Markdown",
		"disable_web_page_preview": true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := t.HTTP
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		out, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("telegram: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return nil
}

// escapeMarkdown does the bare minimum for Telegram's legacy Markdown
// parser: escape `*`, `_`, `` ` ``, `[` which are the format-active
// characters. MarkdownV2 has a longer list but is stricter about ALL
// specials being escaped everywhere — legacy Markdown is more
// forgiving of plain prose. Chief titles and bodies are user prose
// (rarely markdown-active), so this trade-off keeps the delivered
// text readable.
func escapeMarkdown(s string) string {
	// Backtick can't appear inside raw string literals, so use
	// interpreted strings for that one entry.
	r := strings.NewReplacer(
		"*", `\*`,
		"_", `\_`,
		"`", "\\`",
		"[", `\[`,
	)
	return r.Replace(s)
}

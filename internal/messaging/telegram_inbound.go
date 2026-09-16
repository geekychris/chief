package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Listen implements IncomingBackend. Long-polls the Telegram
// getUpdates endpoint and calls `handler` for each incoming message.
// Runs until ctx cancels. Errors that aren't ctx-cancel are logged
// and retried after a backoff.
//
// Voice memos: the polling loop surfaces the voice file_id in
// InboundMessage.Text as "[voice:<file_id>]" — a downstream Whisper
// handler can pick that up. Actual transcription is intentionally
// NOT done here (Whisper transport isn't picked yet; see 04b7).
func (t *TelegramBackend) Listen(ctx context.Context, handler InboundHandler) error {
	if t.Token == "" {
		return fmt.Errorf("telegram backend %q: token required for Listen", t.NameStr)
	}
	base := t.EndpointOverride
	if base == "" {
		base = "https://api.telegram.org"
	}
	client := t.HTTP
	if client == nil {
		// Longer timeout for long-poll — telegram holds the connection
		// up to `timeout` seconds waiting for updates.
		client = &http.Client{Timeout: 65 * time.Second}
	}

	var offset int64
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		updates, newOffset, err := t.getUpdates(ctx, client, base, offset, 30)
		if err != nil {
			slog.Warn("telegram: getUpdates failed", "err", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
			continue
		}
		offset = newOffset
		for _, up := range updates {
			msg := up.Message
			if msg == nil {
				continue
			}
			text := msg.Text
			if text == "" && msg.Voice != nil {
				text = fmt.Sprintf("[voice:%s]", msg.Voice.FileID)
			}
			if text == "" {
				continue
			}
			from := ""
			if msg.From != nil {
				from = strconv.FormatInt(msg.From.ID, 10)
			}
			ts := time.Unix(msg.Date, 0)
			_ = handler(ctx, InboundMessage{
				Backend: t.NameStr,
				From:    from,
				Text:    text,
				Ts:      ts,
			})
		}
	}
}

func (t *TelegramBackend) getUpdates(ctx context.Context, client *http.Client, base string, offset int64, timeoutSec int) ([]tgUpdate, int64, error) {
	url := fmt.Sprintf("%s/bot%s/getUpdates?timeout=%d&offset=%d",
		strings.TrimRight(base, "/"), t.Token, timeoutSec, offset)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, offset, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, offset, err
	}
	defer resp.Body.Close()
	var body tgUpdatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, offset, err
	}
	if !body.OK {
		return nil, offset, fmt.Errorf("telegram: getUpdates returned ok=false")
	}
	next := offset
	for _, up := range body.Result {
		if up.UpdateID >= next {
			next = up.UpdateID + 1
		}
	}
	return body.Result, next, nil
}

// Telegram Bot API wire types — kept private + minimal (only the
// fields we need). Extend as we support more inbound event kinds.
type tgUpdatesResponse struct {
	OK     bool       `json:"ok"`
	Result []tgUpdate `json:"result"`
}

type tgUpdate struct {
	UpdateID int64     `json:"update_id"`
	Message  *tgMessage `json:"message,omitempty"`
}

type tgMessage struct {
	Date int64    `json:"date"`
	Text string   `json:"text,omitempty"`
	From *tgUser  `json:"from,omitempty"`
	Voice *tgFile `json:"voice,omitempty"`
}

type tgUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username,omitempty"`
}

type tgFile struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration,omitempty"`
}

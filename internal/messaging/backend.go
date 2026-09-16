// Package messaging is the plugin layer for outbound side channels
// (ntfy.sh, Pushover, Telegram, Slack, log-to-file, …). Chief itself
// owns the message shape + routing rules; each Backend just knows how
// to deliver a Message to its own external service.
//
// The plugin surface is deliberately small: Send + Name. Backends that
// want inbound (Telegram bots, Slack app events) implement the optional
// IncomingBackend interface separately so the core dispatcher doesn't
// need to know about it.
//
// Wire-up lives in cmd/chiefd — Load config, construct a Backend per
// entry, hand them to a Router, hand the Router to attention.Manager.
// The router runs on the caller's goroutine (typically the flag-raise
// handler); backends are expected to be fire-and-forget bounded by
// their own timeouts so a slow Slack API doesn't wedge the daemon.
package messaging

import (
	"context"
	"time"
)

// Urgency mirrors attention.FlagUrgency; kept as a bare string to avoid
// an import cycle between messaging and attention.
type Urgency string

const (
	UrgencyInfo      Urgency = "info"
	UrgencyAttention Urgency = "attention"
	UrgencyUrgent    Urgency = "urgent"
)

// Message is one outbound event. Backends translate fields into
// whatever their protocol requires (Title→subject line for email,
// Priority mapping for ntfy, threading for Slack, etc.).
type Message struct {
	ProjectID   string
	ProjectName string
	Urgency     Urgency
	Title       string  // short summary; may become the notification title
	Body        string  // full text
	URL         string  // optional deep-link (chief://flag/<id> or a web URL)
	// Ts is when the underlying event happened (typically the flag's
	// created_at). Backends may show this in the delivered message.
	Ts time.Time
	// Kind identifies the origin ("flag.raised", "next_task", "digest").
	// Backends may filter or route on this.
	Kind string
}

// Backend is what a single side channel implements. Send should return
// quickly (< a few seconds); slow backends should implement their own
// timeout via ctx and return early on failure — the router logs and
// moves on rather than propagating the error into the RPC caller.
type Backend interface {
	Name() string
	Send(ctx context.Context, m Message) error
}

// IncomingBackend is the optional inbound-events extension. A Telegram
// or Slack backend implements this to feed user replies back into
// chiefd. Registered separately from Backend so the core dispatcher
// only needs the outbound half.
type IncomingBackend interface {
	Backend
	// Listen blocks until ctx is done. Implementations dial their
	// service, poll or subscribe, and call `handler` for each inbound
	// message. Errors returned should be terminal; recoverable retries
	// are the backend's responsibility.
	Listen(ctx context.Context, handler InboundHandler) error
}

// InboundMessage is what a Backend hands the router for inbound events.
// Chief maps these to chief.answer, chief.next.approve, etc.
type InboundMessage struct {
	Backend string    // name of the receiving backend
	From    string    // opaque user identifier (chat_id, user_id, email)
	Text    string    // raw text; commands parsed downstream
	Ts      time.Time
}

// InboundHandler is invoked by a backend for each inbound message.
// Returning an error signals the backend to (optionally) reply with
// an error notice to the sender.
type InboundHandler func(ctx context.Context, m InboundMessage) error

package notify

import (
	"context"
	"testing"
)

func TestEscapeAS(t *testing.T) {
	cases := []struct{ in, want string }{
		{`hello`, `hello`},
		{`say "hi"`, `say \"hi\"`},
		{`c:\path`, `c:\\path`},
		{"line1\nline2", "line1 · line2"},
		{`mix "quotes" \back`, `mix \"quotes\" \\back`},
	}
	for _, c := range cases {
		got := escapeAS(c.in)
		if got != c.want {
			t.Errorf("escapeAS(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRecordingNotifier_CapturesSends(t *testing.T) {
	r := &RecordingNotifier{}
	ctx := context.Background()
	_ = r.Send(ctx, Notification{Title: "A", Body: "first"})
	_ = r.Send(ctx, Notification{Title: "B", Body: "second", Sound: "Ping"})
	if len(r.Sent) != 2 {
		t.Fatalf("want 2 sent, got %d", len(r.Sent))
	}
	if r.Sent[1].Sound != "Ping" {
		t.Errorf("second notification Sound: got %q, want Ping", r.Sent[1].Sound)
	}
}

func TestNoopNotifier_Silent(t *testing.T) {
	if err := (NoopNotifier{}).Send(context.Background(), Notification{Body: "x"}); err != nil {
		t.Errorf("Noop.Send: unexpected err %v", err)
	}
}

func TestMacNotifier_RejectsEmptyBody(t *testing.T) {
	if err := (MacNotifier{}).Send(context.Background(), Notification{}); err == nil {
		t.Error("expected error for empty body, got nil")
	}
}

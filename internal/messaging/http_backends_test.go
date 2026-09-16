package messaging

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNtfyBackend_SendsPostWithCorrectHeaders(t *testing.T) {
	var gotURL, gotBody string
	gotHeaders := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Path
		for _, h := range []string{"Title", "Priority", "Click", "Tags"} {
			gotHeaders[h] = r.Header.Get(h)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	nb := &NtfyBackend{NameStr: "phone", Server: srv.URL, Topic: "my-topic"}
	err := nb.Send(context.Background(), Message{
		Title:   "Chief · alpha — Attention needed",
		Body:    "Which auth?",
		URL:     "chief://flag/abc",
		Urgency: UrgencyUrgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotURL != "/my-topic" {
		t.Errorf("URL path: got %q want /my-topic", gotURL)
	}
	if gotBody != "Which auth?" {
		t.Errorf("body: got %q", gotBody)
	}
	if gotHeaders["Priority"] != "5" {
		t.Errorf("priority: got %q want 5", gotHeaders["Priority"])
	}
	if gotHeaders["Click"] != "chief://flag/abc" {
		t.Errorf("click: got %q", gotHeaders["Click"])
	}
	if !strings.Contains(gotHeaders["Title"], "Chief") {
		t.Errorf("title: got %q", gotHeaders["Title"])
	}
}

func TestNtfyBackend_TopicRequired(t *testing.T) {
	nb := &NtfyBackend{NameStr: "phone", Server: "http://x"}
	err := nb.Send(context.Background(), Message{Body: "x"})
	if err == nil || !strings.Contains(err.Error(), "topic required") {
		t.Errorf("want topic-required error, got %v", err)
	}
}

func TestNtfyBackend_PropagatesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()
	nb := &NtfyBackend{NameStr: "n", Server: srv.URL, Topic: "t"}
	err := nb.Send(context.Background(), Message{Body: "x", Urgency: UrgencyAttention})
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("want HTTP 500 error, got %v", err)
	}
}

func TestNtfyPriority(t *testing.T) {
	if ntfyPriority(UrgencyUrgent) != "5" {
		t.Error("urgent should map to 5")
	}
	if ntfyPriority(UrgencyAttention) != "3" {
		t.Error("attention should map to 3")
	}
	if ntfyPriority(UrgencyInfo) != "2" {
		t.Error("info should map to 2")
	}
}

func TestPushoverBackend_PostsForm(t *testing.T) {
	var form map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		form = map[string]string{}
		for k := range r.PostForm {
			form[k] = r.PostForm.Get(k)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	pb := &PushoverBackend{
		NameStr: "po", Token: "app-token", User: "user-key",
		EndpointOverride: srv.URL,
	}
	err := pb.Send(context.Background(), Message{
		Title: "T", Body: "hello", Urgency: UrgencyUrgent, URL: "chief://x",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"token", "user", "message", "title", "priority"} {
		if form[k] == "" {
			t.Errorf("form missing %q", k)
		}
	}
	if form["priority"] != "1" {
		t.Errorf("priority: got %q want 1", form["priority"])
	}
	if form["url"] != "chief://x" {
		t.Errorf("url: got %q", form["url"])
	}
}

func TestPushoverBackend_TokenAndUserRequired(t *testing.T) {
	pb := &PushoverBackend{NameStr: "po"}
	err := pb.Send(context.Background(), Message{Body: "x"})
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("want required error, got %v", err)
	}
}

package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func mockServer(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func TestTags(t *testing.T) {
	c := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Fatalf("path %s", r.URL.Path)
		}
		io.WriteString(w, `{"models":[{"name":"llama3.2:1b"},{"name":"qwen2.5:0.5b"}]}`)
	})
	names, err := c.Tags(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "llama3.2:1b" {
		t.Fatalf("got %v", names)
	}
}

func TestTagsError(t *testing.T) {
	c := mockServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	if _, err := c.Tags(context.Background()); err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestHealthy(t *testing.T) {
	up := mockServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	if !up.Healthy(context.Background()) {
		t.Fatal("want healthy")
	}
	down := New("http://127.0.0.1:1")
	if down.Healthy(context.Background()) {
		t.Fatal("want unhealthy")
	}
}

func TestPullStreamsProgress(t *testing.T) {
	c := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		// stream a few NDJSON progress lines
		io.WriteString(w, `{"status":"pulling manifest"}`+"\n")
		io.WriteString(w, `{"status":"downloading","completed":500,"total":1000}`+"\n")
		io.WriteString(w, `{"status":"success","completed":1000,"total":1000}`+"\n")
	})
	var last PullProgress
	var n int
	err := c.Pull(context.Background(), "llama3.2:1b", func(p PullProgress) { last = p; n++ })
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("want 3 progress events, got %d", n)
	}
	if last.Status != "success" || last.Percent() != 100 {
		t.Fatalf("final progress wrong: %+v (%.0f%%)", last, last.Percent())
	}
}

func TestPullReportsError(t *testing.T) {
	c := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"error":"model not found"}`+"\n")
	})
	if err := c.Pull(context.Background(), "nope", nil); err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("want surfaced pull error, got %v", err)
	}
}

func TestChat(t *testing.T) {
	var gotMessages int
	c := mockServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("path %s", r.URL.Path)
		}
		var body struct {
			Messages []Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotMessages = len(body.Messages)
		// 20 tokens in 0.5s -> 40 tok/s
		io.WriteString(w, `{"message":{"role":"assistant","content":"Paris is the capital of France."},"eval_count":20,"eval_duration":500000000}`)
	})
	out, err := c.Chat(context.Background(), "llama3.2:1b", []Message{
		{Role: "user", Content: "capital of France?"},
		{Role: "assistant", Content: "Which country?"},
		{Role: "user", Content: "France"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotMessages != 3 {
		t.Fatalf("server received %d messages, want full history of 3", gotMessages)
	}
	if !strings.Contains(out.Response, "Paris") {
		t.Fatalf("got %q", out.Response)
	}
	if out.EvalCount != 20 || out.TokensPerSec() != 40 {
		t.Fatalf("metrics wrong: %d tokens, %.1f tok/s", out.EvalCount, out.TokensPerSec())
	}
}

func TestChatError(t *testing.T) {
	c := mockServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	if _, err := c.Chat(context.Background(), "x", []Message{{Role: "user", Content: "y"}}); err == nil {
		t.Fatal("expected error")
	}
}

func TestPercentZeroWhenUnknownTotal(t *testing.T) {
	if (PullProgress{Completed: 5, Total: 0}).Percent() != 0 {
		t.Fatal("want 0 when total unknown")
	}
}

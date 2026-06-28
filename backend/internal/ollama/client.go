// Package ollama is a small HTTP client for a local Ollama runtime: list local
// models, pull a model (with streamed progress), and generate a completion.
// The controller manages an Ollama backend through this client, whether Ollama
// runs as a GPU container (Linux) or bare-metal (macOS/Metal).
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client talks to an Ollama server at BaseURL.
type Client struct {
	base string
	http *http.Client
}

// New returns a client for the given base URL (e.g. http://localhost:11434).
func New(base string) *Client {
	return &Client{base: base, http: &http.Client{Timeout: 0}} // no global timeout: pulls/gens are long
}

// Healthy reports whether the Ollama server is reachable.
func (c *Client) Healthy(ctx context.Context) bool {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/tags", nil)
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Tags lists the models already present locally.
func (c *Client) Tags(ctx context.Context) ([]string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/tags", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama tags: status %d", resp.StatusCode)
	}
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out.Models))
	for _, m := range out.Models {
		names = append(names, m.Name)
	}
	return names, nil
}

// PullProgress is a single progress update during a model pull.
type PullProgress struct {
	Status    string
	Completed int64
	Total     int64
}

// Percent returns 0..100 (0 when total is unknown).
func (p PullProgress) Percent() float64 {
	if p.Total <= 0 {
		return 0
	}
	return float64(p.Completed) / float64(p.Total) * 100
}

// Pull downloads a model, invoking onProgress for each streamed update.
func (c *Client) Pull(ctx context.Context, model string, onProgress func(PullProgress)) error {
	body, _ := json.Marshal(map[string]any{"name": model, "stream": true})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/pull", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama pull: status %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev struct {
			Status    string `json:"status"`
			Completed int64  `json:"completed"`
			Total     int64  `json:"total"`
			Error     string `json:"error"`
		}
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.Error != "" {
			return fmt.Errorf("ollama pull: %s", ev.Error)
		}
		if onProgress != nil {
			onProgress(PullProgress{Status: ev.Status, Completed: ev.Completed, Total: ev.Total})
		}
	}
	return sc.Err()
}

// Completion is a generation result plus real throughput metrics from Ollama.
type Completion struct {
	Response     string
	EvalCount    int           // tokens generated
	EvalDuration time.Duration // time spent generating them
}

// TokensPerSec is the real generation throughput, 0 if unknown.
func (c Completion) TokensPerSec() float64 {
	s := c.EvalDuration.Seconds()
	if s <= 0 {
		return 0
	}
	return float64(c.EvalCount) / s
}

// Message is one turn in a conversation.
type Message struct {
	Role    string `json:"role"` // "user" | "assistant" | "system"
	Content string `json:"content"`
}

// Chat runs a multi-turn conversation (the full message history is passed so the
// model has context). It uses Ollama's /api/chat and reports eval_count /
// eval_duration so callers can measure true tokens/sec.
func (c *Client) Chat(ctx context.Context, model string, messages []Message) (Completion, error) {
	body, _ := json.Marshal(map[string]any{"model": model, "messages": messages, "stream": false})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Completion{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
		return Completion{}, fmt.Errorf("ollama chat: status %d: %s", resp.StatusCode, b)
	}
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		EvalCount    int   `json:"eval_count"`
		EvalDuration int64 `json:"eval_duration"` // nanoseconds
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Completion{}, err
	}
	return Completion{Response: out.Message.Content, EvalCount: out.EvalCount, EvalDuration: time.Duration(out.EvalDuration)}, nil
}

package ollama

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"sync"
	"time"
)

// Supervisor owns the lifecycle of a *local* Ollama backend. When the AI
// module is installed the controller starts `ollama serve` itself and keeps
// it running; on uninstall (or controller shutdown) it stops the process it
// started. An Ollama that was already running externally is left alone —
// the supervisor only ever kills what it spawned.
type Supervisor struct {
	client *Client
	binary string // resolved path to the ollama binary ("" until looked up)

	mu       sync.Mutex
	cmd      *exec.Cmd
	stopping bool
	logf     func(string, ...any)
}

// NewSupervisor supervises the backend behind client. logf may be nil.
func NewSupervisor(client *Client, logf func(string, ...any)) *Supervisor {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Supervisor{client: client, logf: logf}
}

// Manageable reports whether baseURL points at this host — the only case
// where starting a process locally can serve it.
func Manageable(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1", "":
		return true
	}
	return false
}

// EnsureUp makes the backend reachable: if it already answers, nothing to do
// (external instance — not managed); otherwise start `ollama serve` and wait
// for it to become healthy.
func (s *Supervisor) EnsureUp(ctx context.Context) error {
	if s.client.Healthy(ctx) {
		return nil
	}
	s.mu.Lock()
	if s.cmd != nil {
		// we already own a process; give it a moment below
		s.mu.Unlock()
	} else {
		bin, err := exec.LookPath("ollama")
		if err != nil {
			s.mu.Unlock()
			return fmt.Errorf("ollama binary not found — install it first (macOS: `brew install ollama`, Linux: https://ollama.com/download)")
		}
		s.binary = bin
		cmd := exec.Command(bin, "serve")
		if err := cmd.Start(); err != nil {
			s.mu.Unlock()
			return fmt.Errorf("start ollama: %w", err)
		}
		s.cmd = cmd
		s.stopping = false
		s.logf("ai backend: started `ollama serve` (pid %d)", cmd.Process.Pid)
		go s.reap(cmd)
		s.mu.Unlock()
	}

	// wait for health (ollama binds quickly; allow a generous first boot)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if s.client.Healthy(ctx) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return fmt.Errorf("ollama started but did not become healthy in time")
}

// reap waits for the child and logs unexpected exits. Restarting is left to
// the next EnsureUp (the manager calls it when the module needs the backend),
// so a crashed backend never flaps in a tight loop.
func (s *Supervisor) reap(cmd *exec.Cmd) {
	err := cmd.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == cmd {
		s.cmd = nil
	}
	if !s.stopping {
		s.logf("ai backend: ollama exited unexpectedly: %v", err)
	}
}

// Stop terminates the Ollama process the supervisor started, if any. External
// instances are untouched.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	cmd := s.cmd
	s.stopping = true
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	s.logf("ai backend: stopping `ollama serve` (pid %d)", cmd.Process.Pid)
	_ = cmd.Process.Signal(interruptSignal)
	done := make(chan struct{})
	go func() {
		for {
			s.mu.Lock()
			gone := s.cmd == nil
			s.mu.Unlock()
			if gone {
				close(done)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
	}
}

// Managed reports whether the supervisor currently owns a running process.
func (s *Supervisor) Managed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cmd != nil
}

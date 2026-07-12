//go:build !windows

package ollama

import "syscall"

// interruptSignal asks a child to shut down gracefully before we resort to Kill.
var interruptSignal = syscall.SIGTERM

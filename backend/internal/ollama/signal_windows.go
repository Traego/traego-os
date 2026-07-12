//go:build windows

package ollama

import "os"

// Windows has no SIGTERM; Kill is the only option.
var interruptSignal = os.Kill

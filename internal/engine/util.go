package engine

import (
	"context"
	"io"
	"os"
	"time"
)

// pluginLog is where km.log writes. It defaults to stderr so a misbehaving
// plugin is visible during development; the TUI replaces it with a buffer.
var pluginLog io.Writer = os.Stderr

// SetLog redirects km.log output.
func SetLog(w io.Writer) { pluginLog = w }

// openRead opens a file for reading.
func openRead(path string) (*os.File, error) { return os.Open(path) }

// contextWithTimeout is a tiny indirection so tests can stub the clock.
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

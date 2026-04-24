package operator

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// Terminal is an Operator that reads from stdin and writes to stdout.
// It's safe to instantiate once per session; methods are goroutine-safe.
type Terminal struct {
	mu     sync.Mutex
	reader *bufio.Reader
	out    io.Writer
	errW   io.Writer
}

// NewTerminal wraps the process's stdin/stdout/stderr.
func NewTerminal() *Terminal {
	return &Terminal{
		reader: bufio.NewReader(os.Stdin),
		out:    os.Stdout,
		errW:   os.Stderr,
	}
}

// NewTerminalFrom lets tests inject alternate streams.
func NewTerminalFrom(in io.Reader, out, errW io.Writer) *Terminal {
	return &Terminal{reader: bufio.NewReader(in), out: out, errW: errW}
}

// Ask writes the prompt and any options to the configured out stream,
// then reads a single line from in. Blocks until the user types a
// newline, ctx is cancelled, or the reader reports EOF. Returns the
// trimmed input; on ctx cancel returns ctx.Err().
func (t *Terminal) Ask(ctx context.Context, prompt string, options []Option) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprintln(t.out, prompt)
	if len(options) > 0 {
		for _, o := range options {
			if o.Hint != "" {
				fmt.Fprintf(t.out, "  [%s] %s\n", o.Label, o.Hint)
			} else {
				fmt.Fprintf(t.out, "  [%s]\n", o.Label)
			}
		}
		fmt.Fprint(t.out, "? ")
	} else {
		fmt.Fprint(t.out, "> ")
	}
	// Read one line, honoring ctx cancellation
	ch := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		line, err := t.reader.ReadString('\n')
		if err != nil {
			errCh <- err
			return
		}
		ch <- strings.TrimRight(line, "\r\n")
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-errCh:
		return "", err
	case s := <-ch:
		return s, nil
	}
}

// Notify writes message to the Terminal's out stream with a severity
// prefix keyed by kind. Goroutine-safe.
func (t *Terminal) Notify(kind NotifyKind, message string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	prefix := ""
	switch kind {
	case KindWarn:
		prefix = "WARN: "
	case KindError:
		prefix = "ERROR: "
	case KindSuccess:
		prefix = "OK: "
	case KindInfo:
		// No prefix — informational messages print as-is.
	}
	fmt.Fprintln(t.out, prefix+message)
}

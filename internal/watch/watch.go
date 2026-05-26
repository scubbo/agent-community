// Package watch tails a community's messages.jsonl and emits new messages
// as they arrive, with no buffering lag.
//
// The prototype this replaces relied on `tail -f | grep` and hit a known
// failure mode: tail block-buffers when its stdout is a pipe, so message N
// surfaces only when message N+1 arrives. Since this package owns the entire
// read path -- file -> decode -> formatted output -> stdout -- we can flush
// after every line and avoid the lag entirely. Do not introduce intermediate
// pipes here.
package watch

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/jackjackson/agent-community/internal/message"
)

// Options configures Run.
type Options struct {
	CommunityRoot string
	ExcludeAuthor string    // Messages with this Author are skipped (typically the caller's own name).
	From          time.Time // Only emit messages at or after this timestamp. Zero = start at end of file.
	Format        Formatter // How each message becomes an output line. Defaults to PrettyLine.
}

// Formatter renders a message as a single line of output (no trailing
// newline). Run adds the trailing newline.
type Formatter func(*message.Message) string

// PrettyLine is the default human-readable single-line format. Approximates
// the prototype's columnar feel.
func PrettyLine(m *message.Message) string {
	short := m.Timestamp.UTC().Format("15:04:05Z")
	var b strings.Builder
	b.WriteString(short)
	b.WriteString(" | ")
	b.WriteString(padName(m.Author))
	b.WriteString(" | ")
	b.WriteString(m.Body)
	if m.Context != "" {
		b.WriteString(" | ")
		b.WriteString(m.Context)
	}
	return b.String()
}

func padName(name string) string {
	const w = 7
	if len(name) >= w {
		return name
	}
	return name + strings.Repeat(" ", w-len(name))
}

// JSONLine emits the raw JSONL line. Useful when downstream consumers want
// to parse structured fields.
func JSONLine(m *message.Message) string {
	b, _ := m.Encode()
	return string(b)
}

// Run tails the message log and writes formatted lines to `out`. Returns
// when ctx is cancelled. Stdout is flushed after every line via an explicit
// bufio.Writer.Flush so notifications surface immediately.
//
// Important: do not pipe `out` through any other writer that adds its own
// buffering. The whole point of this package is to remove buffering lag.
func Run(ctx context.Context, opts Options, out io.Writer) error {
	if opts.Format == nil {
		opts.Format = PrettyLine
	}

	path := message.MessagesPath(opts.CommunityRoot)

	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			return fmt.Errorf("create empty messages file: %w", err)
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if opts.From.IsZero() {
		if _, err := f.Seek(0, io.SeekEnd); err != nil {
			return err
		}
	} else {
		if err := seekToTimestamp(f, opts.From); err != nil {
			return err
		}
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()
	if err := watcher.Add(path); err != nil {
		return err
	}

	// bufio.NewWriter then explicit Flush after each line. This gives us a
	// buffered Write for efficiency without the lag of leaving bytes pending.
	bw := bufio.NewWriter(out)
	br := bufio.NewReader(f)

	emit := func(line []byte) error {
		m, err := message.Decode(line)
		if err != nil {
			return nil
		}
		if opts.ExcludeAuthor != "" && m.Author == opts.ExcludeAuthor {
			return nil
		}
		if !opts.From.IsZero() && m.Timestamp.Before(opts.From) {
			return nil
		}
		if _, err := bw.WriteString(opts.Format(m)); err != nil {
			return err
		}
		if err := bw.WriteByte('\n'); err != nil {
			return err
		}
		return bw.Flush()
	}

	if err := drain(br, emit); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			if err := drain(br, emit); err != nil {
				return err
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			return err
		}
	}
}

// drain reads complete lines from r and passes each to emit. Returns when no
// more complete lines are available (i.e. ReadBytes returns io.EOF).
//
// On partial-line reads (no trailing newline), the bytes are buffered by the
// bufio.Reader and will be combined with the next chunk.
func drain(r *bufio.Reader, emit func([]byte) error) error {
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
			if len(line) > 0 {
				if emitErr := emit(line); emitErr != nil {
					return emitErr
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// seekToTimestamp reads forward from the start of the file until it finds
// the first message at or after `from`, then leaves the file position there.
//
// O(N) over file size. Acceptable for v1 -- the alternative (timestamp
// index) is YAGNI.
func seekToTimestamp(f *os.File, from time.Time) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	br := bufio.NewReader(f)
	offset := int64(0)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := line
			if trimmed[len(trimmed)-1] == '\n' {
				trimmed = trimmed[:len(trimmed)-1]
			}
			if len(trimmed) > 0 {
				m, decErr := message.Decode(trimmed)
				if decErr == nil && !m.Timestamp.Before(from) {
					if _, err := f.Seek(offset, io.SeekStart); err != nil {
						return err
					}
					return nil
				}
			}
			offset += int64(len(line))
		}
		if err == io.EOF {
			_, err := f.Seek(0, io.SeekEnd)
			return err
		}
		if err != nil {
			return err
		}
	}
}

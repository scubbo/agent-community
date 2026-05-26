package message

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// MessagesFile is the on-disk filename. Kept in this package so callers
// don't need to import internal/community just for the filename.
const MessagesFile = "messages.jsonl"

// Append writes one Message to the given community root's messages.jsonl.
//
// Uses O_APPEND to leverage the kernel's atomic append guarantee for writes
// shorter than PIPE_BUF (4KB on macOS/Linux). For longer messages, the worst
// case is interleaving with another writer; this package does not lock.
func Append(communityRoot string, m *Message) error {
	path := filepath.Join(communityRoot, MessagesFile)
	if err := os.MkdirAll(communityRoot, 0o755); err != nil {
		return err
	}

	line, err := m.Encode()
	if err != nil {
		return err
	}
	line = append(line, '\n')

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Write(line); err != nil {
		return err
	}
	return nil
}

// ReadOptions filters the messages returned by ReadAll.
type ReadOptions struct {
	Since time.Time // Zero means no lower bound.
	Limit int       // 0 means no limit.
}

// ReadAll returns the messages in the log matching the options. Order is
// chronological (file order). Limit is applied after filtering and returns
// the most recent N messages.
func ReadAll(communityRoot string, opts ReadOptions) ([]*Message, error) {
	path := filepath.Join(communityRoot, MessagesFile)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []*Message
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		m, err := Decode(line)
		if err != nil {
			return nil, fmt.Errorf("decode %s line %d: %w", path, lineNum, err)
		}
		if !opts.Since.IsZero() && m.Timestamp.Before(opts.Since) {
			continue
		}
		out = append(out, m)
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return nil, err
	}
	if opts.Limit > 0 && len(out) > opts.Limit {
		out = out[len(out)-opts.Limit:]
	}
	return out, nil
}

// MessagesPath returns the absolute path of messages.jsonl for a community.
// Exported so the watch package can open it directly.
func MessagesPath(communityRoot string) string {
	return filepath.Join(communityRoot, MessagesFile)
}

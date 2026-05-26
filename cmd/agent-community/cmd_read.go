package main

import (
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/jackjackson/agent-community/internal/community"
	"github.com/jackjackson/agent-community/internal/message"
)

func cmdRead(args []string) error {
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	since := fs.String("since", "", "Only show messages at or after this RFC3339 timestamp.")
	limit := fs.Int("limit", 0, "Show at most N most-recent messages. 0 = all.")
	emitJSON := fs.Bool("json", false, "Emit raw JSONL (one JSON object per line) instead of pretty-printed.")
	workspace := fs.String("workspace", "", "Workspace to resolve from. Default: current working directory.")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-community read [--since TS] [--limit N] [--json] [--workspace PATH]")
		fs.PrintDefaults()
	}
	if err := parse(fs, args); err != nil {
		return usageErr("%v", err)
	}

	ws, err := resolveWorkspace(*workspace)
	if err != nil {
		return err
	}

	resolved, err := community.Resolve(ws)
	if err != nil {
		if errors.Is(err, community.ErrNoCommunity) {
			return noCommunityErr(err)
		}
		return err
	}

	var sinceTime time.Time
	if *since != "" {
		t, err := time.Parse(time.RFC3339, *since)
		if err != nil {
			return validationErr("--since must be RFC3339 (e.g. 2026-05-25T00:00:00Z): %v", err)
		}
		sinceTime = t
	}

	msgs, err := message.ReadAll(resolved.Root, message.ReadOptions{
		Since: sinceTime,
		Limit: *limit,
	})
	if err != nil {
		return err
	}

	for _, m := range msgs {
		if *emitJSON {
			line, _ := m.Encode()
			fmt.Println(string(line))
			continue
		}
		fmt.Printf("%s | %-7s | %s", m.Timestamp.Format("2006-01-02 15:04:05Z"), m.Author, m.Body)
		if m.Context != "" {
			fmt.Printf("\n                          context: %s", m.Context)
		}
		fmt.Println()
	}
	return nil
}

package main

import (
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/scubbo/agent-community/internal/community"
	"github.com/scubbo/agent-community/internal/message"
)

func cmdRead(args []string) error {
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	since := fs.String("since", "", "Only show messages at or after this RFC3339 timestamp.")
	limit := fs.Int("limit", 0, "Show at most N most-recent messages. 0 = all.")
	emitJSON := fs.Bool("json", false, "Emit raw JSONL (one JSON object per line) instead of pretty-printed.")
	workspace := fs.String("workspace", "", "Workspace to resolve from. Default: current working directory.")

	// Colony filters
	msgType := fs.String("type", "", "Filter to this message type (heartbeat, progress, etc.)")
	minUrgency := fs.Int("min-urgency", 0, "Only show messages with urgency >= N")
	from := fs.String("from", "", "Only show messages from this author")
	toFilter := fs.String("to", "", "Only show messages to this recipient (or 'all' for broadcasts)")

	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-community read [--since TS] [--limit N] [--json] [--workspace PATH]")
		fmt.Fprintln(fs.Output(), "       agent-community read --type TYPE [--min-urgency N] [--from AUTHOR] [--to RECIPIENT]")
		fmt.Fprintln(fs.Output(), "")
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
		Limit: 0, // We'll apply limit after filtering
	})
	if err != nil {
		return err
	}

	// Apply colony filters
	filtered := msgs[:0]
	for _, m := range msgs {
		// Filter by type
		if *msgType != "" && string(m.Type) != *msgType {
			continue
		}
		// Filter by minimum urgency
		if *minUrgency > 0 && m.Urgency < *minUrgency {
			continue
		}
		// Filter by author
		if *from != "" && m.Author != *from {
			continue
		}
		// Filter by recipient
		if *toFilter != "" {
			if *toFilter == "all" && m.To != "" {
				continue
			} else if *toFilter != "all" && m.To != *toFilter {
				continue
			}
		}
		filtered = append(filtered, m)
	}

	// Apply limit after filtering
	if *limit > 0 && len(filtered) > *limit {
		filtered = filtered[len(filtered)-*limit:]
	}

	for _, m := range filtered {
		if *emitJSON {
			line, _ := m.Encode()
			fmt.Println(string(line))
			continue
		}
		// Format output based on whether it's a colony message
		if m.Type != "" {
			// Colony message: show type, urgency, and recipient
			toDisplay := "all"
			if m.To != "" {
				toDisplay = m.To
			}
			fmt.Printf("%s | %-7s | [%s u%d→%s] %s",
				m.Timestamp.Format("2006-01-02 15:04:05Z"), m.Author,
				m.Type, m.Urgency, toDisplay, m.Body)
		} else {
			// Classic message
			fmt.Printf("%s | %-7s | %s", m.Timestamp.Format("2006-01-02 15:04:05Z"), m.Author, m.Body)
		}
		if m.Context != "" {
			fmt.Printf("\n                          context: %s", m.Context)
		}
		fmt.Println()
	}
	return nil
}

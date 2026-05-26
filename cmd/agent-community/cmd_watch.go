package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackjackson/agent-community/internal/community"
	"github.com/jackjackson/agent-community/internal/identity"
	"github.com/jackjackson/agent-community/internal/watch"
)

func cmdWatch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	from := fs.String("from", "", "Replay messages at or after this RFC3339 timestamp before tailing. Default: tail from end.")
	emitJSON := fs.Bool("json", false, "Emit raw JSONL (one JSON object per line) instead of pretty-printed.")
	workspace := fs.String("workspace", "", "Workspace to resolve from. Default: current working directory.")
	noFilter := fs.Bool("no-filter-self", false, "Include messages from this workspace's claimed identity. Default: exclude self.")
	ignoreNoCommunity := fs.Bool("ignore-no-community", false, "Exit silently (code 0) if no community is resolved. Used by the Claude monitor so non-community workspaces don't error.")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-community watch [--from TS] [--json] [--workspace PATH] [--no-filter-self] [--ignore-no-community]")
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
			if *ignoreNoCommunity {
				return nil
			}
			return noCommunityErr(err)
		}
		return err
	}

	var exclude string
	if !*noFilter {
		if name, idErr := identity.Whoami(ws); idErr == nil {
			exclude = name
		}
	}

	var fromTime time.Time
	if *from != "" {
		t, err := time.Parse(time.RFC3339, *from)
		if err != nil {
			return validationErr("--from must be RFC3339: %v", err)
		}
		fromTime = t
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		cancel()
	}()

	opts := watch.Options{
		CommunityRoot: resolved.Root,
		ExcludeAuthor: exclude,
		From:          fromTime,
	}
	if *emitJSON {
		opts.Format = watch.JSONLine
	}

	return watch.Run(ctx, opts, os.Stdout)
}

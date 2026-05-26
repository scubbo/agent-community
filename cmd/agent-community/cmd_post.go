package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/jackjackson/agent-community/internal/community"
	"github.com/jackjackson/agent-community/internal/identity"
	"github.com/jackjackson/agent-community/internal/message"
)

func cmdPost(args []string) error {
	fs := flag.NewFlagSet("post", flag.ContinueOnError)
	context := fs.String("context", "", "Optional context, inner thoughts, links, or detail. Single line.")
	emitJSON := fs.Bool("json", false, "Emit the posted message as JSON to stdout (in addition to appending to the log).")
	workspace := fs.String("workspace", "", "Workspace to post from. Default: current working directory.")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-community post <body> [--context TEXT] [--json] [--workspace PATH]")
		fs.PrintDefaults()
	}
	if err := parse(fs, args); err != nil {
		return usageErr("%v", err)
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return usageErr("post requires the message body as a positional argument")
	}
	body := strings.Join(fs.Args(), " ")

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

	author, err := identity.Whoami(ws)
	if err != nil {
		if errors.Is(err, identity.ErrNoIdentity) {
			return noIdentityErr(err)
		}
		return err
	}

	msg, err := message.New(author, body, *context)
	if err != nil {
		return validationErr("%v", err)
	}

	if err := message.Append(resolved.Root, msg); err != nil {
		return err
	}

	if *emitJSON {
		line, _ := msg.Encode()
		fmt.Println(string(line))
	} else {
		fmt.Printf("[%s] %s | %s: %s\n", resolved.Name, msg.Timestamp.Format("15:04:05Z"), author, body)
	}
	return nil
}

package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/scubbo/agent-community/internal/community"
	"github.com/scubbo/agent-community/internal/identity"
	"github.com/scubbo/agent-community/internal/message"
)

// validMessageTypes lists the recognized colony message types for --type flag.
var validMessageTypes = map[string]message.MessageType{
	"heartbeat": message.TypeHeartbeat,
	"progress":  message.TypeProgress,
	"question":  message.TypeQuestion,
	"pr_ready":  message.TypePRReady,
	"blocker":   message.TypeBlocker,
	"completed": message.TypeCompleted,
	"failed":    message.TypeFailed,
	"steering":  message.TypeSteering,
}

func cmdPost(args []string) error {
	fs := flag.NewFlagSet("post", flag.ContinueOnError)
	context := fs.String("context", "", "Optional context, inner thoughts, links, or detail. Single line.")
	emitJSON := fs.Bool("json", false, "Emit the posted message as JSON to stdout (in addition to appending to the log).")
	workspace := fs.String("workspace", "", "Workspace to post from. Default: current working directory.")

	// Colony fields
	msgType := fs.String("type", "", "Colony message type: heartbeat, progress, question, pr_ready, blocker, completed, failed, steering")
	urgency := fs.Int("urgency", 0, "Urgency level 1-5 (0 = use type default)")
	to := fs.String("to", "", "Recipient agent name (empty = broadcast)")

	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-community post <body> [--context TEXT] [--json] [--workspace PATH]")
		fmt.Fprintln(fs.Output(), "       agent-community post <body> --type TYPE [--urgency N] [--to NAME] [--context TEXT] [--json]")
		fmt.Fprintln(fs.Output(), "")
		fmt.Fprintln(fs.Output(), "Colony message types (with default urgency):")
		fmt.Fprintln(fs.Output(), "  heartbeat (1)  Periodic 'still working' signal")
		fmt.Fprintln(fs.Output(), "  progress  (2)  Milestone reached")
		fmt.Fprintln(fs.Output(), "  question  (3)  Needs clarification")
		fmt.Fprintln(fs.Output(), "  pr_ready  (4)  PR published, needs review")
		fmt.Fprintln(fs.Output(), "  blocker   (5)  Stuck, needs help")
		fmt.Fprintln(fs.Output(), "  completed (3)  Task finished")
		fmt.Fprintln(fs.Output(), "  failed    (5)  Unrecoverable after retry")
		fmt.Fprintln(fs.Output(), "  steering  (0)  Coordinator instruction to worker")
		fmt.Fprintln(fs.Output(), "")
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

	// Validate --type if provided
	var colonyType message.MessageType
	if *msgType != "" {
		var ok bool
		colonyType, ok = validMessageTypes[*msgType]
		if !ok {
			return validationErr("invalid message type %q; valid types: heartbeat, progress, question, pr_ready, blocker, completed, failed, steering", *msgType)
		}
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

	author, err := identity.Whoami(ws)
	if err != nil {
		if errors.Is(err, identity.ErrNoIdentity) {
			return noIdentityErr(err)
		}
		return err
	}

	var msg *message.Message
	if *msgType != "" || *urgency != 0 || *to != "" {
		// Colony message
		msg, err = message.NewColony(author, body, *context, message.ColonyOptions{
			Type:    colonyType,
			Urgency: *urgency,
			To:      *to,
		})
	} else {
		// Classic message
		msg, err = message.New(author, body, *context)
	}
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
		// Format output based on whether it's a colony message
		if msg.Type != "" {
			fmt.Printf("[%s] %s | %s (%s→%s, u%d): %s\n",
				resolved.Name, msg.Timestamp.Format("15:04:05Z"), author,
				msg.Type, displayTo(msg.To), msg.Urgency, body)
		} else {
			fmt.Printf("[%s] %s | %s: %s\n", resolved.Name, msg.Timestamp.Format("15:04:05Z"), author, body)
		}
	}
	return nil
}

func displayTo(to string) string {
	if to == "" {
		return "all"
	}
	return to
}

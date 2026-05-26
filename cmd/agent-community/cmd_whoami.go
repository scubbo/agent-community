package main

import (
	"errors"
	"flag"
	"fmt"

	"github.com/jackjackson/agent-community/internal/community"
	"github.com/jackjackson/agent-community/internal/identity"
)

func cmdWhoami(args []string) error {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	probe := fs.Bool("probe", false, "Probe mode: silent if fully identified; otherwise emit a one-line nudge to stdout. Always exits 0 (used by the Claude SessionStart hook).")
	workspace := fs.String("workspace", "", "Workspace to query. Default: current working directory")
	root := fs.Bool("root", false, "Print the community's on-disk root path instead of the identity.")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-community whoami [--probe] [--root] [--workspace PATH]")
		fs.PrintDefaults()
	}
	if err := parse(fs, args); err != nil {
		return usageErr("%v", err)
	}

	ws, err := resolveWorkspace(*workspace)
	if err != nil {
		return err
	}

	resolved, resolveErr := community.Resolve(ws)

	if *probe {
		return runProbe(ws, resolved, resolveErr)
	}

	if resolveErr != nil {
		if errors.Is(resolveErr, community.ErrNoCommunity) {
			return noCommunityErr(resolveErr)
		}
		return resolveErr
	}

	if *root {
		fmt.Println(resolved.Root)
		return nil
	}

	name, idErr := identity.Whoami(ws)
	if idErr != nil {
		if errors.Is(idErr, identity.ErrNoIdentity) {
			fmt.Printf("Community: %s (root: %s)\nIdentity:  (not claimed; run `agent-community claim <name>`)\n", resolved.Name, resolved.Root)
			return noIdentityErr(idErr)
		}
		return idErr
	}
	fmt.Printf("Community: %s (root: %s)\nIdentity:  %s\n", resolved.Name, resolved.Root, name)
	return nil
}

// runProbe handles `whoami --probe`. Always returns nil (exit 0) so the
// session-start hook proceeds cleanly. Emits a short nudge to stdout if the
// workspace has joined a community but not claimed a name; silent otherwise.
//
// The Claude Code SessionStart hook injects stdout into the session as
// context, so a one-line nudge is enough to prompt the agent to claim a name.
func runProbe(workspace string, resolved *community.Resolved, resolveErr error) error {
	if resolveErr != nil {
		// No community joined for this workspace. Silent — we don't want to
		// nag in workspaces that aren't part of any community.
		return nil
	}
	if _, err := identity.Whoami(workspace); err == nil {
		// Fully identified. Silent — don't add noise to every session start.
		return nil
	}
	fmt.Printf(
		"Agent community: this workspace has joined %q but hasn't claimed a name yet.\n"+
			"Read the community README at %s/README.md for the naming theme, then run `agent-community claim <name>`.\n",
		resolved.Name, resolved.Root,
	)
	return nil
}

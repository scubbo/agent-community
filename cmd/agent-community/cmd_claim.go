package main

import (
	"errors"
	"flag"
	"fmt"

	"github.com/scubbo/agent-community/internal/community"
	"github.com/scubbo/agent-community/internal/identity"
)

func cmdClaim(args []string) error {
	fs := flag.NewFlagSet("claim", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "Workspace to claim in. Default: current working directory")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-community claim <name> [--workspace PATH]")
		fs.PrintDefaults()
	}
	if err := parse(fs, args); err != nil {
		return usageErr("%v", err)
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return usageErr("claim requires exactly one positional argument: the agent name")
	}
	name := fs.Arg(0)

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

	cfg, err := community.ReadConfig(resolved.Root)
	if err != nil {
		return fmt.Errorf("read community config: %w", err)
	}

	if err := identity.ClaimName(identity.ClaimOptions{
		Name:          name,
		Workspace:     ws,
		CommunityRoot: resolved.Root,
		MaxLength:     cfg.Naming.MaxLength,
	}); err != nil {
		return validationErr("%v", err)
	}

	fmt.Printf("Claimed %q in community %q for workspace %s\n", name, resolved.Name, ws)
	return nil
}

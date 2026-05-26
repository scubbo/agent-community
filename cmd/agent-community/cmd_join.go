package main

import (
	"flag"
	"fmt"

	"github.com/jackjackson/agent-community/internal/community"
)

func cmdJoin(args []string) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "Workspace to join. Default: current working directory")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-community join <name> [--workspace PATH]")
		fs.PrintDefaults()
	}
	if err := parse(fs, args); err != nil {
		return usageErr("%v", err)
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return usageErr("join requires exactly one positional argument: the community name")
	}
	name := fs.Arg(0)

	workdir, err := community.Join(community.JoinOptions{
		Name:      name,
		Workspace: *workspace,
	})
	if err != nil {
		return validationErr("%v", err)
	}

	fmt.Printf("Joined community %q in workspace %s\n", name, workdir)
	fmt.Println("Next: claim an agent name for this workspace.")
	fmt.Printf("  agent-community claim <your-name>\n")
	return nil
}

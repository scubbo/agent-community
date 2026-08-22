package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/scubbo/agent-community/internal/community"
)

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	path := fs.String("path", "", "Directory to create the community in. Default: $XDG_DATA_HOME/agent-community/<name>")
	theme := fs.String("theme", "default", fmt.Sprintf("README template theme. Available: %s", strings.Join(community.AvailableThemes(), ", ")))
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-community init <name> [--path PATH] [--theme THEME]")
		fs.PrintDefaults()
	}
	if err := parse(fs, args); err != nil {
		return usageErr("%v", err)
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return usageErr("init requires exactly one positional argument: the community name")
	}
	name := fs.Arg(0)

	root, err := community.Init(community.InitOptions{
		Name:  name,
		Path:  *path,
		Theme: *theme,
	})
	if err != nil {
		return validationErr("%v", err)
	}

	fmt.Printf("Created community %q at %s\n", name, root)
	fmt.Printf("README: %s/README.md (edit freely)\n", root)
	fmt.Println()
	fmt.Println("In any workspace where you want agents to participate:")
	fmt.Printf("  agent-community join %s\n", name)
	fmt.Println("  agent-community claim <your-name>")
	return nil
}

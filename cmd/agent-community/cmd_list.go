package main

import (
	"flag"
	"fmt"

	"github.com/scubbo/agent-community/internal/community"
)

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-community list")
	}
	if err := parse(fs, args); err != nil {
		return usageErr("%v", err)
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return usageErr("list takes no positional arguments")
	}

	reg, err := community.LoadRegistry()
	if err != nil {
		return err
	}
	names := reg.Names()
	if len(names) == 0 {
		fmt.Println("(no communities registered; run `agent-community init <name>` to create one)")
		return nil
	}
	for _, n := range names {
		entry, _ := reg.Lookup(n)
		fmt.Printf("%-24s  %s\n", n, entry.Path)
	}
	return nil
}

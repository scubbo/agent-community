package main

import (
	"flag"
	"fmt"

	"github.com/jackjackson/agent-community/internal/community"
)

func cmdThemes(args []string) error {
	fs := flag.NewFlagSet("themes", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-community themes")
	}
	if err := parse(fs, args); err != nil {
		return usageErr("%v", err)
	}
	for _, t := range community.AvailableThemes() {
		fmt.Println(t)
	}
	return nil
}

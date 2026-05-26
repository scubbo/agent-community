package main

import (
	"flag"
	"fmt"

	"github.com/jackjackson/agent-community/internal/claude"
)

func cmdClaudeInstall(args []string) error {
	fs := flag.NewFlagSet("claude-install", flag.ContinueOnError)
	target := fs.String("target", "", "Target plugins directory. Default: ~/.claude/plugins")
	force := fs.Bool("force", false, "Overwrite existing installation.")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-community claude-install [--target PATH] [--force]")
		fs.PrintDefaults()
	}
	if err := parse(fs, args); err != nil {
		return usageErr("%v", err)
	}

	dest, err := claude.Install(claude.InstallOptions{
		TargetDir: *target,
		Force:     *force,
	})
	if err != nil {
		return err
	}
	fmt.Printf("Installed Claude Code plugin at %s\n", dest)
	fmt.Println("Restart Claude Code (or run /reload-plugins) to pick it up.")
	return nil
}

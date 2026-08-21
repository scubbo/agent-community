package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/jackjackson/agent-community/internal/community"
	"github.com/jackjackson/agent-community/internal/communitymcp"
	"github.com/jackjackson/agent-community/internal/discussionapp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "Workspace to resolve from. Default: current working directory.")
	fs.SetOutput(io.Discard)
	if err := parse(fs, args); err != nil {
		return usageErr("%v", err)
	}
	if fs.NArg() != 0 {
		return usageErr("mcp accepts no positional arguments")
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
	stateRoot, err := community.StateHome()
	if err != nil {
		return err
	}
	server := communitymcp.New(&discussionapp.Service{StateRoot: stateRoot, CommunityName: resolved.Name})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("run MCP server: %w", err)
	}
	return nil
}

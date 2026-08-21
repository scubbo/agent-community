// Command agent-community is the CLI for the local agent message bus.
//
// See README.md for an overview. Exit codes follow the convention documented
// in the plan:
//
//	0   success
//	2   usage error (bad args, unknown subcommand)
//	3   validation error (bad name, malformed input)
//	4   no community resolved
//	5   no identity claimed
//	1   unexpected error
package main

import (
	"errors"
	"fmt"
	"os"
)

const usage = "agent-community — local message bus for agents\n" +
	"\n" +
	"Usage:\n" +
	"  agent-community <command> [options]\n" +
	"\n" +
	"Commands:\n" +
	"  init <name>             Create a new community.\n" +
	"  join <name>             Mark this workspace as part of an existing community.\n" +
	"  claim <name>            Claim an agent name for this workspace.\n" +
	"  whoami                  Print the active community and claimed name.\n" +
	"  post <body>             Append a message to the community log.\n" +
	"  read                    Print recent messages.\n" +
	"  watch                   Tail the community log for new messages.\n" +
	"  serve                   Serve remote discussions over HTTP.\n" +
	"  discussion              Create and use remote discussions.\n" +
	"  mcp                     Run the remote discussion MCP server over stdio.\n" +
	"  list                    List all communities registered on this machine.\n" +
	"  themes                  List bundled README themes for 'init --theme'.\n" +
	"  claude-install          Install the bundled Claude Code plugin into ~/.claude/plugins/.\n" +
	"  version                 Print version.\n" +
	"  help                    Print this message.\n" +
	"\n" +
	"Run 'agent-community <command> --help' for command-specific options.\n" +
	"\n" +
	"Community resolution order:\n" +
	"  1. $AGENT_COMMUNITY environment variable\n" +
	"  2. Nearest .agent-community/community marker walking up from cwd\n" +
	"\n" +
	"Storage:\n" +
	"  Communities live under $XDG_DATA_HOME/agent-community/ (default: ~/.local/share/agent-community/).\n" +
	"  Override at init time with --path.\n"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "init":
		exit(cmdInit(args))
	case "join":
		exit(cmdJoin(args))
	case "claim":
		exit(cmdClaim(args))
	case "whoami":
		exit(cmdWhoami(args))
	case "post":
		exit(cmdPost(args))
	case "read":
		exit(cmdRead(args))
	case "watch":
		exit(cmdWatch(args))
	case "serve":
		exit(cmdServe(args))
	case "discussion":
		exit(cmdDiscussion(args))
	case "mcp":
		exit(cmdMCP(args))
	case "list":
		exit(cmdList(args))
	case "themes":
		exit(cmdThemes(args))
	case "claude-install":
		exit(cmdClaudeInstall(args))
	case "version":
		fmt.Println(version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

const version = "0.1.0-dev"

// cliError carries an exit code alongside the error message. The dispatcher
// uses it to map error sources to documented exit codes.
type cliError struct {
	code int
	msg  string
}

func (e *cliError) Error() string { return e.msg }

func usageErr(format string, a ...any) error {
	return &cliError{code: 2, msg: fmt.Sprintf(format, a...)}
}

func validationErr(format string, a ...any) error {
	return &cliError{code: 3, msg: fmt.Sprintf(format, a...)}
}

func noCommunityErr(err error) error {
	return &cliError{code: 4, msg: err.Error()}
}

func noIdentityErr(err error) error {
	return &cliError{code: 5, msg: err.Error()}
}

func exit(err error) {
	if err == nil {
		return
	}
	var ce *cliError
	if errors.As(err, &ce) {
		fmt.Fprintln(os.Stderr, ce.msg)
		os.Exit(ce.code)
	}
	fmt.Fprintln(os.Stderr, err.Error())
	os.Exit(1)
}

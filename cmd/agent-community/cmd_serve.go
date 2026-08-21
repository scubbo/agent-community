package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jackjackson/agent-community/internal/community"
	"github.com/jackjackson/agent-community/internal/communityserver"
)

func cmdServe(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runServe(ctx, args, os.Stdout)
}

func runServe(ctx context.Context, args []string, output io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:7337", "Local TCP address for the discussion HTTP API.")
	publicURL := fs.String("public-url", "", "Public HTTPS URL that forwards to this server. Required.")
	workspace := fs.String("workspace", "", "Workspace to resolve from. Default: current working directory.")
	controlSocket := fs.String("control-socket", "", "Unix socket for local management. Default: a community-scoped path under XDG state.")
	allowLoopbackHTTP := fs.Bool("allow-loopback-http", false, "Allow an HTTP public URL only when its host is loopback. For local development.")
	fs.SetOutput(io.Discard)
	if err := parse(fs, args); err != nil {
		return usageErr("%v", err)
	}
	if fs.NArg() != 0 {
		return usageErr("serve accepts no positional arguments")
	}
	if *publicURL == "" {
		return usageErr("serve requires --public-url")
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
	socketPath := *controlSocket
	if socketPath == "" {
		socketPath = filepath.Join(stateRoot, "control", resolved.Name+".sock")
	}
	runtime, err := communityserver.Start(communityserver.RuntimeOptions{
		CommunityName:     resolved.Name,
		CommunityRoot:     resolved.Root,
		StateRoot:         stateRoot,
		ListenAddress:     *listen,
		PublicURL:         *publicURL,
		SocketPath:        socketPath,
		AllowLoopbackHTTP: *allowLoopbackHTTP,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Serving community %q\n", resolved.Name)
	fmt.Fprintf(output, "Local:  %s\n", runtime.LocalURL())
	fmt.Fprintf(output, "Public: %s\n", *publicURL)

	waited := make(chan error, 1)
	go func() { waited <- runtime.Wait() }()
	select {
	case serveErr := <-waited:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return errors.Join(serveErr, runtime.Close(shutdownCtx))
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		closeErr := runtime.Close(shutdownCtx)
		return errors.Join(closeErr, <-waited)
	}
}

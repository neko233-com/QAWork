package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/neko233-com/QAWork/internal/mcp"
)

// cmdMCP serves the Model Context Protocol on stdio. Nothing but JSON-RPC
// may go to stdout: the agent reads it as the protocol channel.
func cmdMCP() int {
	srv := mcp.NewServer()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Fprintln(os.Stderr, "qawork mcp: stdio 已就绪")
	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mcp 结束:", err)
		return 1
	}
	return 0
}

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"nacfson_pipeline/services/debug-mcp/internal/audit"
	"nacfson_pipeline/services/debug-mcp/internal/config"
	"nacfson_pipeline/services/debug-mcp/internal/kube"
	"nacfson_pipeline/services/debug-mcp/internal/mcp"
	"nacfson_pipeline/services/debug-mcp/internal/tools"
)

func main() {
	var (
		serveUnix  string
		connect    string
		kubeconfig string
		socketMode string
	)
	flag.StringVar(&serveUnix, "serve-unix", "", "Unix domain socket path to listen on")
	flag.StringVar(&connect, "connect", "", "Unix domain socket path to bridge over stdio")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to read-only kubeconfig file")
	flag.StringVar(&socketMode, "socket-mode", "0660", "Octal permissions mode for Unix socket")
	flag.Parse()

	if (serveUnix == "" && connect == "") || (serveUnix != "" && connect != "") {
		fmt.Fprintln(os.Stderr, "exactly one of --serve-unix or --connect is required")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if connect != "" {
		if err := mcp.ConnectUnix(ctx, connect, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "bridge error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if kubeconfig == "" {
		fmt.Fprintln(os.Stderr, "--kubeconfig is required with --serve-unix")
		os.Exit(2)
	}
	parsedMode, err := strconv.ParseUint(socketMode, 8, 32)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --socket-mode %q: %v\n", socketMode, err)
		os.Exit(2)
	}

	store, err := config.NewStore(kubeconfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load kubeconfig failed: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	auditor := audit.NewLogger(os.Stderr)
	hupCh := make(chan os.Signal, 1)
	signal.Notify(hupCh, syscall.SIGHUP)
	defer signal.Stop(hupCh)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hupCh:
				if rErr := store.Reload(); rErr != nil {
					auditor.Log(audit.Entry{
						Tool:   "daemon_reload",
						Status: "error",
						Error:  rErr.Error(),
					})
				} else {
					auditor.Log(audit.Entry{
						Tool:   "daemon_reload",
						Status: "ok",
					})
				}
			}
		}
	}()

	client := kube.NewClient(store)
	registry := tools.NewRegistry(client)
	srv := mcp.NewServer(registry, auditor)
	if err := srv.ServeUnix(ctx, serveUnix, os.FileMode(parsedMode)); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

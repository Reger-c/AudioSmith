package main

// "AudioSmith --mcp-stdio": the MCP stdio shim (M5, thread #20). MCP
// clients that only start servers as commands run the app's executable
// with this flag; it relays their JSON-RPC lines to the running app's
// /mcp and exits when they close stdin. It needs the app running (with
// the Local API on), which it finds through <data>/api.json. It never
// opens a window.
//
//	--client <id>   the agent's name, for its bound voice (also AUDIOSMITH_CLIENT)
//	AUDIOSMITH_API_KEY   the API key, when the app requires one
//	AUDIOSMITH_API_URL   the API's base URL, instead of the port file

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/hehehai/AudioSmith/internal/api"
	mcpsrv "github.com/hehehai/AudioSmith/internal/mcp"
)

// runMCPStdio runs the shim when the command line asks for it, and
// reports whether it did (main then returns).
func runMCPStdio() bool {
	if len(os.Args) < 2 || os.Args[1] != "--mcp-stdio" {
		return false
	}
	o, err := mcpStdioOptions(os.Args[2:], os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "audiosmith:", err)
		os.Exit(2)
	}
	o.Log = os.Stderr
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := mcpsrv.Proxy(ctx, os.Stdin, os.Stdout, o); err != nil {
		fmt.Fprintln(os.Stderr, "audiosmith:", err)
		os.Exit(1)
	}
	return true
}

// mcpStdioOptions reads the shim's flags and environment.
func mcpStdioOptions(args []string, getenv func(string) string) (mcpsrv.ProxyOptions, error) {
	o := mcpsrv.ProxyOptions{URL: getenv("AUDIOSMITH_API_URL"), Key: getenv("AUDIOSMITH_API_KEY"), Client: getenv("AUDIOSMITH_CLIENT")}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--client":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--client needs a name")
			}
			i++
			o.Client = args[i]
		default:
			return o, fmt.Errorf("unknown argument %q (usage: AudioSmith --mcp-stdio [--client <name>])", args[i])
		}
	}
	if dir, err := os.UserConfigDir(); err == nil {
		// The app's data folder: mygo's PathUserData for "AudioSmith"
		// (dev builds share it, see main).
		o.PortFile = filepath.Join(dir, "AudioSmith", api.PortFileName)
	}
	return o, nil
}

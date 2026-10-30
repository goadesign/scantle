// Command scantle runs the Scantle MCP software factory control-plane server
// and CLI utilities (`scantle serve` and `scantle diff`).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	genscantle "github.com/goadesign/scantle/gen/scantle"
	"github.com/goadesign/scantle/internal/controlplane"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "scantle: %v\n", err)
		os.Exit(1)
	}
}

// run dispatches the scantle subcommands (`serve` and `diff`).
func run(args []string) error {
	sub := "serve"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "serve":
		return runServe(args)
	case "diff":
		return runDiff(args)
	default:
		return fmt.Errorf("unknown subcommand %q (expected \"serve\" or \"diff\")", sub)
	}
}

// runServe starts the Scantle HTTP and MCP JSON-RPC server.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8090", "HTTP and MCP listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cp := controlplane.New(controlplane.Options{})
	srv := &http.Server{
		Addr:              *addr,
		Handler:           controlplane.NewHTTPHandler(cp),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}

// runDiff compares two Goa design files and two go.mod files and writes the
// structured diff JSON report to stdout.
func runDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	oldDesignPath := fs.String("old-design", "", "Path to old Goa design.go")
	newDesignPath := fs.String("new-design", "", "Path to new Goa design.go")
	oldModPath := fs.String("old-gomod", "", "Path to old go.mod")
	newModPath := fs.String("new-gomod", "", "Path to new go.mod")
	if err := fs.Parse(args); err != nil {
		return err
	}
	oldDesign, err := readOptionalFile(*oldDesignPath)
	if err != nil {
		return err
	}
	newDesign, err := readOptionalFile(*newDesignPath)
	if err != nil {
		return err
	}
	oldMod, err := readOptionalFile(*oldModPath)
	if err != nil {
		return err
	}
	newMod, err := readOptionalFile(*newModPath)
	if err != nil {
		return err
	}
	cp := controlplane.New(controlplane.Options{})
	res, err := cp.Diff(context.Background(), &genscantle.DiffPayload{
		OldDesign: oldDesign,
		NewDesign: newDesign,
		OldGomod:  oldMod,
		NewGomod:  newMod,
	})
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

// readOptionalFile reads path when non-empty or returns an empty string.
func readOptionalFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

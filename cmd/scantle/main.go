// Command scantle runs the Scantle MCP software factory control-plane server
// and CLI utilities (`scantle serve` and `scantle diff`).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	genscantle "github.com/goadesign/scantle/gen/scantle"
	"github.com/goadesign/scantle/internal/controlplane"
	"github.com/goadesign/scantle/internal/diff"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "scantle: %v\n", err)
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

// runServe starts the Scantle HTTP and MCP JSON-RPC server protected by a
// verified bearer-token authenticator.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8090", "HTTP and MCP listen address")
	principalID := fs.String("principal", "", "Authenticated operator principal ID")
	authToken := fs.String("auth-token", os.Getenv("SCANTLE_AUTH_TOKEN"), "Bearer token required on HTTP and MCP requests")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*principalID) == "" || strings.TrimSpace(*authToken) == "" {
		return errors.New("serve requires -principal and -auth-token (or SCANTLE_AUTH_TOKEN)")
	}
	auth := controlplane.NewTokenAuthenticator(map[string]controlplane.Principal{
		strings.TrimSpace(*authToken): {ID: strings.TrimSpace(*principalID)},
	})
	cp := controlplane.New(controlplane.Options{})
	srv := &http.Server{
		Addr:              *addr,
		Handler:           controlplane.NewHTTPHandler(cp, auth),
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

// runDiff compares two design modules on disk, each read into a snapshot with
// readModuleSnapshot, and writes the structured diff JSON report to stdout.
func runDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	oldDir := fs.String("old-dir", "", "root directory of the previous design module (holding go.mod and design/)")
	newDir := fs.String("new-dir", "", "root directory of the candidate design module (holding go.mod and design/)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *oldDir == "" || *newDir == "" {
		return errors.New("--old-dir and --new-dir are required")
	}
	oldFiles, err := readModuleSnapshot(*oldDir)
	if err != nil {
		return err
	}
	newFiles, err := readModuleSnapshot(*newDir)
	if err != nil {
		return err
	}
	rep, err := diff.Compare(context.Background(), oldFiles, newFiles)
	if err != nil {
		return err
	}
	res := &genscantle.DiffResult{
		HasChanges:           rep.HasChanges,
		AddedDeclarations:    rep.AddedDeclarations,
		RemovedDeclarations:  rep.RemovedDeclarations,
		ModifiedDeclarations: rep.ModifiedDeclarations,
		VersionDeltas:        rep.VersionDeltas,
		Summary:              rep.Summary,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

// readModuleSnapshot reads the module rooted at dir into a design snapshot:
// its go.mod, go.sum and every non-test .go file, keyed by slash-separated
// path relative to dir. Hidden, vendor and testdata directories and nested
// modules are skipped. A symbolic link anywhere in the module is an error, and
// files are opened without following links, so the snapshot holds only files
// that really are inside dir.
func readModuleSnapshot(dir string) (map[string]string, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link; a design snapshot holds only regular files", p)
		}
		if d.IsDir() {
			name := d.Name()
			switch {
			case rel == ".":
				return nil
			case strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata":
				return filepath.SkipDir
			}
			if _, err := os.Lstat(filepath.Join(p, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		slash := filepath.ToSlash(rel)
		keep := slash == "go.mod" || slash == "go.sum" ||
			(strings.HasSuffix(slash, ".go") && !strings.HasSuffix(slash, "_test.go"))
		if !keep || !d.Type().IsRegular() {
			return nil
		}
		f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		defer f.Close()
		b, err := io.ReadAll(f)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		files[slash] = string(b)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

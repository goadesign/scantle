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

	"goa.design/goa-ai/features/model/anthropic"
	"goa.design/goa-ai/features/model/gateway"
	"goa.design/goa-ai/features/model/openai"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	genscantle "github.com/goadesign/scantle/gen/scantle"
	"github.com/goadesign/scantle/internal/controlplane"
	"github.com/goadesign/scantle/internal/diff"
	"github.com/goadesign/scantle/internal/swarm"
)

// serveConfig holds the optional `scantle serve` flags that choose the CP1
// model and the swarm provider profiles. Zero values mean "not configured".
type serveConfig struct {
	// gatewayAddr is the host:port of a Goa-AI model gateway reached over
	// plaintext gRPC. It excludes provider and modelName.
	gatewayAddr string
	// provider is "anthropic" or "openai" for direct provider calls; it
	// requires modelName and the provider's API key environment variable.
	provider string
	// modelName is the provider model identifier used in direct mode.
	modelName string
	// swarmProfilesPath is the path of a JSON array of swarm.ProviderProfile.
	swarmProfilesPath string
}

// apiKeyEnv maps each supported direct model provider to the environment
// variable that holds its API key. Keys are read only from the environment so
// they never appear in process listings or shell history.
var apiKeyEnv = map[string]string{
	"anthropic": "ANTHROPIC_API_KEY",
	"openai":    "OPENAI_API_KEY",
}

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
func runServe(args []string) (err error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8090", "HTTP and MCP listen address")
	principalID := fs.String("principal", "", "Authenticated operator principal ID")
	authToken := fs.String("auth-token", os.Getenv("SCANTLE_AUTH_TOKEN"), "Bearer token required on HTTP and MCP requests")
	var cfg serveConfig
	fs.StringVar(&cfg.gatewayAddr, "model-gateway-addr", "", "CP1 model gateway host:port (plaintext gRPC; excludes -model-provider and -model-name)")
	fs.StringVar(&cfg.provider, "model-provider", "", "CP1 model provider: anthropic or openai (key from ANTHROPIC_API_KEY or OPENAI_API_KEY)")
	fs.StringVar(&cfg.modelName, "model-name", "", "CP1 model name for -model-provider")
	fs.StringVar(&cfg.swarmProfilesPath, "swarm-profiles", "", "JSON file listing the coding-agent provider profiles used for CP2 swarm plans")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*principalID) == "" || strings.TrimSpace(*authToken) == "" {
		return errors.New("serve requires -principal and -auth-token (or SCANTLE_AUTH_TOKEN)")
	}
	opts, closeModel, err := serveOptions(cfg, os.Getenv)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, closeModel()) }()
	auth := controlplane.NewTokenAuthenticator(map[string]controlplane.Principal{
		strings.TrimSpace(*authToken): {ID: strings.TrimSpace(*principalID)},
	})
	cp := controlplane.New(opts)
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

// serveOptions builds the control-plane Options for `scantle serve` from cfg.
// getenv supplies provider API keys. The returned close function releases the
// model gateway connection, if any, and returns nil when no connection was
// opened. With no model flags the Analyzer stays nil, and with no
// -swarm-profiles the profiles stay nil, so the server still answers diff,
// golden-path and compiled_rule requests without credentials.
func serveOptions(cfg serveConfig, getenv func(string) string) (controlplane.Options, func() error, error) {
	profiles, err := loadSwarmProfiles(cfg.swarmProfilesPath)
	if err != nil {
		return controlplane.Options{}, nil, err
	}
	analyzer, closeModel, err := newCP1Analyzer(cfg, getenv)
	if err != nil {
		return controlplane.Options{}, nil, err
	}
	return controlplane.Options{Analyzer: analyzer, SwarmProfiles: profiles}, closeModel, nil
}

// newCP1Analyzer returns the CP1 analyzer selected by cfg: a model gateway
// client when gatewayAddr is set, a direct provider client when provider and
// modelName are set, or nil when neither is set. Mixed or partial model flags
// and a missing provider API key are errors; key values never appear in them.
func newCP1Analyzer(cfg serveConfig, getenv func(string) string) (controlplane.CP1Analyzer, func() error, error) {
	noop := func() error { return nil }
	switch {
	case cfg.gatewayAddr != "" && (cfg.provider != "" || cfg.modelName != ""):
		return nil, nil, errors.New("-model-gateway-addr cannot be combined with -model-provider or -model-name; the gateway chooses the model")
	case cfg.gatewayAddr != "":
		conn, err := grpc.NewClient(cfg.gatewayAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return nil, nil, fmt.Errorf("connect model gateway %q: %w", cfg.gatewayAddr, err)
		}
		client, err := gateway.NewGRPCClient(conn)
		if err != nil {
			return nil, nil, errors.Join(fmt.Errorf("model gateway client: %w", err), conn.Close())
		}
		return controlplane.NewModelCP1Analyzer(client), conn.Close, nil
	case cfg.provider == "" && cfg.modelName == "":
		return nil, noop, nil
	case cfg.provider == "" || cfg.modelName == "":
		return nil, nil, errors.New("-model-provider and -model-name must be set together")
	}
	env, ok := apiKeyEnv[cfg.provider]
	if !ok {
		return nil, nil, fmt.Errorf("unsupported -model-provider %q (expected \"anthropic\" or \"openai\")", cfg.provider)
	}
	key := getenv(env)
	if key == "" {
		return nil, nil, fmt.Errorf("-model-provider %s requires %s", cfg.provider, env)
	}
	newClient := anthropic.NewFromAPIKey
	if cfg.provider == "openai" {
		newClient = openai.NewFromAPIKey
	}
	client, err := newClient(key, cfg.modelName)
	if err != nil {
		return nil, nil, fmt.Errorf("%s model client: %w", cfg.provider, err)
	}
	return controlplane.NewModelCP1Analyzer(client), noop, nil
}

// loadSwarmProfiles decodes the JSON array of swarm.ProviderProfile at path,
// rejecting unknown fields and trailing data. An empty path means no profiles
// are configured. Profile contents are checked by swarm.Plan at CP2.
func loadSwarmProfiles(path string) ([]swarm.ProviderProfile, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read -swarm-profiles: %w", err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var profiles []swarm.ProviderProfile
	if err := dec.Decode(&profiles); err != nil {
		return nil, fmt.Errorf("decode -swarm-profiles %s: %w", path, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode -swarm-profiles %s: trailing data after the profile array", path)
	}
	return profiles, nil
}

// Package designeval evaluates Go/Goa/Goa-AI design sources by compiling a
// standalone evaluator binary and executing it inside verify/candidate.Exec's
// OS-enforced single-process sandbox, using Goa-AI's canonical preparation and
// JSON Schema builders (`codegen/agent.Prepare` and `codegen/jsonschema.Build`).
package designeval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	genscantle "github.com/goadesign/scantle/gen/scantle"
	"github.com/goadesign/scantle/verify/candidate"
	"golang.org/x/mod/modfile"
)

type (
	// Result holds the canonical declarations, C4 topology, agent policies, and
	// tool contracts extracted from an evaluated Goa/Goa-AI design.
	Result struct {
		// SystemName is the evaluated API name or primary service name.
		SystemName string `json:"system_name"`
		// Declarations maps each scoped declaration key to its canonical evaluated contract signature.
		Declarations map[string]string `json:"declarations"`
		// Containers lists the C4 containers derived from evaluated Services, Agents, and Toolsets.
		Containers []*genscantle.C4ContainerItem `json:"containers"`
		// Edges lists the validated directed contract edges between C4 containers.
		Edges []*genscantle.C4EdgeItem `json:"edges"`
		// AgentPolicies lists the evaluated Goa-AI RunPolicy settings per agent.
		AgentPolicies []*genscantle.AgentPolicyItem `json:"agent_policies"`
		// Tools lists the evaluated Goa-AI tool contracts across all declared toolsets.
		Tools []*genscantle.ToolContractItem `json:"tools"`
	}

	// toolchainEnv holds the resolved Go toolchain root, module cache directory,
	// and private sandboxed build work directory for candidate.Build.
	toolchainEnv struct {
		// goroot is the resolved Go toolchain root.
		goroot string
		// modCache is the read-only Go module cache directory.
		modCache string
		// workDir is the private writable directory for GOCACHE, HOME, and TMPDIR.
		workDir string
	}
)

var (
	// resolveToolchainEnv caches the resolved GOROOT, GOMODCACHE, and private
	// sandboxed build cache directory used by candidate.Build.
	resolveToolchainEnv = sync.OnceValues(func() (toolchainEnv, error) {
		out, err := exec.Command("go", "env", "GOROOT", "GOMODCACHE").Output()
		if err != nil {
			return toolchainEnv{}, fmt.Errorf("resolve go env GOROOT GOMODCACHE: %w", err)
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) < 2 || strings.TrimSpace(lines[0]) == "" || strings.TrimSpace(lines[1]) == "" {
			return toolchainEnv{}, fmt.Errorf("unexpected go env output %q", string(out))
		}
		workDir, err := os.MkdirTemp("", "scantle-designeval-work-*")
		if err != nil {
			return toolchainEnv{}, fmt.Errorf("create designeval work dir: %w", err)
		}
		return toolchainEnv{
			goroot:   strings.TrimSpace(lines[0]),
			modCache: strings.TrimSpace(lines[1]),
			workDir:  workDir,
		}, nil
	})
)

// Evaluate compiles a standalone Goa/Goa-AI design evaluator binary inside
// verify/candidate.Build's OS-enforced build sandbox and runs it inside
// verify/candidate.Exec's OS-enforced single-process runtime sandbox, returning
// the evaluated Result.
func Evaluate(ctx context.Context, designSource string) (Result, error) {
	if strings.TrimSpace(designSource) == "" {
		return Result{
			Declarations:  make(map[string]string),
			Containers:    []*genscantle.C4ContainerItem{},
			Edges:         []*genscantle.C4EdgeItem{},
			AgentPolicies: []*genscantle.AgentPolicyItem{},
			Tools:         []*genscantle.ToolContractItem{},
		}, nil
	}

	normalizedSrc, err := normalizeDesignPackage(designSource)
	if err != nil {
		return Result{}, fmt.Errorf("parse design source: %w", err)
	}

	modRoot, modPath, err := locateModuleRoot()
	if err != nil {
		return Result{}, err
	}
	tc, err := resolveToolchainEnv()
	if err != nil {
		return Result{}, err
	}

	tmpDir, err := os.MkdirTemp("", "scantle-designeval-*")
	if err != nil {
		return Result{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() {
		if rmErr := os.RemoveAll(tmpDir); rmErr != nil {
			return
		}
	}()

	buildDir := filepath.Join(tmpDir, "build")
	outDir := filepath.Join(tmpDir, "out")
	storeDir := filepath.Join(tmpDir, "store")
	stateDir := filepath.Join(tmpDir, "state")
	for _, d := range []string{buildDir, outDir, storeDir, stateDir, filepath.Join(buildDir, "design")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return Result{}, fmt.Errorf("mkdir %q: %w", d, err)
		}
	}

	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(modRoot, name))
		if err != nil {
			return Result{}, fmt.Errorf("read %s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(buildDir, name), data, 0o600); err != nil {
			return Result{}, fmt.Errorf("write %s: %w", name, err)
		}
	}

	if err := os.WriteFile(filepath.Join(buildDir, "design", "design.go"), []byte(normalizedSrc), 0o600); err != nil {
		return Result{}, fmt.Errorf("write design.go: %w", err)
	}
	harnessMain := strings.ReplaceAll(harnessMainTemplate, "{{DESIGN_IMPORT}}", strconv.Quote(modPath+"/design"))
	if err := os.WriteFile(filepath.Join(buildDir, "main.go"), []byte(harnessMain), 0o600); err != nil {
		return Result{}, fmt.Errorf("write evaluator main.go: %w", err)
	}

	rawBin := filepath.Join(outDir, "designeval-bin")
	buildLog := filepath.Join(tmpDir, "build.log")
	if err := candidate.Build(ctx, candidate.BuildSpec{
		ModuleDir: buildDir,
		Package:   ".",
		Out:       rawBin,
		GOROOT:    tc.goroot,
		ModCache:  tc.modCache,
		WorkDir:   tc.workDir,
		LogFile:   buildLog,
	}); err != nil {
		logBytes, readErr := os.ReadFile(buildLog)
		if readErr != nil {
			return Result{}, fmt.Errorf("compile Goa design: %w", err)
		}
		return Result{}, fmt.Errorf("compile Goa design: %w (%s)", err, strings.TrimSpace(string(logBytes)))
	}

	art, err := candidate.Pin(storeDir, rawBin)
	if err != nil {
		return Result{}, fmt.Errorf("pin design evaluator binary: %w", err)
	}
	defer func() {
		if rmErr := candidate.RemoveStore(storeDir); rmErr != nil {
			return
		}
	}()

	outFile := filepath.Join(stateDir, "evaluated.json")
	logFile := filepath.Join(tmpDir, "designeval.log")
	proc, err := candidate.Exec(ctx, art, candidate.Spec{
		Args:     []string{"-out", outFile},
		StateDir: stateDir,
		LogFile:  logFile,
	})
	if err != nil {
		return Result{}, fmt.Errorf("exec sandboxed design evaluator: %w", err)
	}
	if waitErr := proc.Wait(); waitErr != nil {
		excerpt, _, exErr := proc.Excerpt(16 << 10)
		if exErr != nil {
			return Result{}, fmt.Errorf("evaluate Goa design in sandbox: %w", waitErr)
		}
		return Result{}, fmt.Errorf("evaluate Goa design in sandbox: %w (%s)", waitErr, strings.TrimSpace(excerpt))
	}

	rawJSON, err := os.ReadFile(outFile)
	if err != nil {
		return Result{}, fmt.Errorf("read evaluated design output: %w", err)
	}
	var res Result
	if err := json.Unmarshal(rawJSON, &res); err != nil {
		return Result{}, fmt.Errorf("decode evaluated Goa design: %w", err)
	}
	return res, nil
}

// normalizeDesignPackage parses src as Go source and ensures its package clause
// is `package design` so the temporary evaluator harness can import it cleanly.
func normalizeDesignPackage(src string) (string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "design.go", src, parser.ParseComments)
	if err != nil {
		return "", err
	}
	if file.Name != nil && file.Name.Name != "design" {
		file.Name = ast.NewIdent("design")
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, fset, file); err != nil {
			return "", err
		}
		return buf.String(), nil
	}
	return src, nil
}

// locateModuleRoot resolves the repository root containing go.mod and returns
// its directory path and declared module path.
func locateModuleRoot() (string, string, error) {
	var candidates []string
	if envRoot := strings.TrimSpace(os.Getenv("SCANTLE_MODULE_ROOT")); envRoot != "" {
		candidates = append(candidates, envRoot)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		candidates = append(candidates, filepath.Dir(file))
	}

	for _, start := range candidates {
		dir := start
		for {
			modFile := filepath.Join(dir, "go.mod")
			if raw, err := os.ReadFile(modFile); err == nil {
				if modPath := modfile.ModulePath(raw); modPath != "" {
					return dir, modPath, nil
				}
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return "", "", errors.New("go.mod not found via SCANTLE_MODULE_ROOT, working directory, or caller path")
}

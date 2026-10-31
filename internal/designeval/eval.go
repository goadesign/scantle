// Package designeval evaluates Go/Goa/Goa-AI/Model design sources by compiling a
// standalone evaluator binary and executing it inside verify/candidate.Exec's
// OS-enforced single-process sandbox. Registered generators return source bytes;
// the evaluator hashes those bytes and reads Goa-AI's existing tool catalogs.
// Protobuf comparisons include Goa's .proto sources, without invoking protoc or
// claiming that generated source equality proves runtime compatibility.
package designeval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/verify/candidate"
	"golang.org/x/mod/modfile"
)

type (
	// Result holds the canonical declarations, C4 topology, agent policies, and
	// tool contracts extracted from an evaluated Goa/Goa-AI/Model design.
	Result struct {
		// SystemName is the evaluated API name, Model system name, or primary service name.
		SystemName string `json:"system_name"`
		// Declarations contains generated source digests under Generated:<path>
		// and domain, Model, and agent-policy declaration records under scoped names.
		Declarations map[string]string `json:"declarations"`
		// Containers lists the C4 containers derived from evaluated Services, Agents, Toolsets, and Model containers.
		Containers []*gensurla.C4ContainerItem `json:"containers"`
		// Edges lists the validated directed contract edges between C4 containers.
		Edges []*gensurla.C4EdgeItem `json:"edges"`
		// AgentPolicies lists the evaluated Goa-AI RunPolicy settings per agent.
		AgentPolicies []*gensurla.AgentPolicyItem `json:"agent_policies"`
		// Tools lists the public tools selected by each agent, with compiled input
		// choices; it does not claim which binding the application installs.
		Tools []*gensurla.ToolContractItem `json:"tools"`
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
		workDir, err := os.MkdirTemp("", "surla-designeval-work-*")
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

// Evaluate evaluates the Goa/Goa-AI/Model design in a module snapshot and
// returns its declarations, C4 topology, agent policies and tool contracts.
//
// files is the module's exact content keyed by module-relative path (see
// validateSnapshot). The snapshot is staged byte for byte; no host path it
// names is read. Its dependencies are resolved only from its go.mod and
// go.sum, and evaluation fails rather than use different versions (see
// prepareHarnessModule). The evaluator program is then compiled with
// candidate.Build and run with candidate.Exec, so neither compiling nor
// running the design can read evaluator files or reach the network.
func Evaluate(ctx context.Context, files map[string]string) (Result, error) {
	target, err := validateSnapshot(files)
	if err != nil {
		return Result{}, err
	}
	evalMod, err := evaluatorGoMod()
	if err != nil {
		return Result{}, err
	}
	tc, err := resolveToolchainEnv()
	if err != nil {
		return Result{}, err
	}

	tmpDir, err := os.MkdirTemp("", "surla-designeval-*")
	if err != nil {
		return Result{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	buildDir := filepath.Join(tmpDir, "build")
	outDir := filepath.Join(tmpDir, "out")
	storeDir := filepath.Join(tmpDir, "store")
	stateDir := filepath.Join(tmpDir, "state")
	logDir := filepath.Join(tmpDir, "logs")
	for _, d := range []string{buildDir, outDir, storeDir, stateDir, logDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return Result{}, fmt.Errorf("mkdir %q: %w", d, err)
		}
	}
	if err := stageSnapshot(filepath.Join(buildDir, "target"), files); err != nil {
		return Result{}, err
	}
	designImport := strconv.Quote(target.Module.Mod.Path + "/design")
	harnessMain := strings.ReplaceAll(harnessMainTemplate, "{{DESIGN_IMPORT}}", designImport)
	harnessMain = strings.ReplaceAll(harnessMain, "{{GEN_IMPORT}}", strconv.Quote(target.Module.Mod.Path+"/gen"))
	if err := os.WriteFile(filepath.Join(buildDir, "main.go"), []byte(harnessMain), 0o600); err != nil {
		return Result{}, fmt.Errorf("write evaluator main.go: %w", err)
	}
	gocmd := goCommand{bin: filepath.Join(tc.goroot, "bin", "go")}
	if err := prepareHarnessModule(ctx, gocmd, buildDir, target, evalMod, logDir); err != nil {
		return Result{}, err
	}

	rawBin := filepath.Join(outDir, "designeval-bin")
	buildLog := filepath.Join(tmpDir, "build.log")
	if err := candidate.Build(ctx, candidate.BuildSpec{
		SourceDir: buildDir,
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

// evaluatorGoMod returns the evaluator's own go.mod, which supplies the
// generator versions a target that does not require them is evaluated with.
func evaluatorGoMod() (*modfile.File, error) {
	root, _, err := locateModuleRoot()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, fmt.Errorf("read evaluator go.mod: %w", err)
	}
	mod, err := modfile.Parse("go.mod", raw, nil)
	if err != nil {
		return nil, fmt.Errorf("parse evaluator go.mod: %w", err)
	}
	return mod, nil
}

// locateModuleRoot resolves the repository root containing go.mod and returns
// its directory path and declared module path.
func locateModuleRoot() (string, string, error) {
	var candidates []string
	if envRoot := strings.TrimSpace(os.Getenv("SURLA_MODULE_ROOT")); envRoot != "" {
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
	return "", "", errors.New("go.mod not found via SURLA_MODULE_ROOT, working directory, or caller path")
}

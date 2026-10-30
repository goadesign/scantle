// Package designeval evaluates Go/Goa/Goa-AI/Model design sources by compiling a
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
	"go/scanner"
	"go/token"
	"io"
	"io/fs"
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
	"golang.org/x/mod/module"
	"golang.org/x/mod/sumdb/dirhash"
	"golang.org/x/tools/txtar"
)

type (
	// Result holds the canonical declarations, C4 topology, agent policies, and
	// tool contracts extracted from an evaluated Goa/Goa-AI/Model design.
	Result struct {
		// SystemName is the evaluated API name, Model system name, or primary service name.
		SystemName string `json:"system_name"`
		// Declarations maps each scoped declaration key to its canonical evaluated contract signature.
		Declarations map[string]string `json:"declarations"`
		// Containers lists the C4 containers derived from evaluated Services, Agents, Toolsets, and Model containers.
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

// Evaluate compiles a standalone Goa/Goa-AI/Model design evaluator binary inside
// verify/candidate.Build's OS-enforced build sandbox and runs it inside
// verify/candidate.Exec's OS-enforced single-process runtime sandbox, returning
// the evaluated Result.
func Evaluate(ctx context.Context, designSource string) (Result, error) {
	return EvaluateModule(ctx, designSource, "")
}

// EvaluateModule compiles and evaluates designSource (which may be a single Go
// file string, a multi-file txtar/concatenated Go source string, or a path on
// disk to a design file, design directory, or module root) against the target
// goMod directives inside verify/candidate's build and execution sandboxes.
func EvaluateModule(ctx context.Context, designSource, goMod string) (Result, error) {
	if strings.TrimSpace(designSource) == "" {
		return Result{
			Declarations:  make(map[string]string),
			Containers:    []*genscantle.C4ContainerItem{},
			Edges:         []*genscantle.C4EdgeItem{},
			AgentPolicies: []*genscantle.AgentPolicyItem{},
			Tools:         []*genscantle.ToolContractItem{},
		}, nil
	}

	modRoot, baseModPath, err := locateModuleRoot()
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

	targetModDir, embeddedGoMod, embeddedGoSum, err := materializeDesignSources(buildDir, designSource)
	if err != nil {
		return Result{}, fmt.Errorf("parse design source: %w", err)
	}
	if strings.TrimSpace(goMod) == "" && embeddedGoMod != "" {
		goMod = embeddedGoMod
	}

	effectiveModPath, err := prepareBuildGoMod(buildDir, modRoot, baseModPath, targetModDir, goMod, embeddedGoSum, tc.modCache)
	if err != nil {
		return Result{}, err
	}

	harnessMain := strings.ReplaceAll(harnessMainTemplate, "{{DESIGN_IMPORT}}", strconv.Quote(effectiveModPath+"/design"))
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

// materializeDesignSources populates buildDir with the target design package
// (`design/*.go`) and any sibling Go packages from a filesystem path, a txtar
// multi-file bundle, a multi-clause concatenated Go source string, or a single
// Go file string.
func materializeDesignSources(buildDir, designSource string) (targetModDir, embeddedGoMod, embeddedGoSum string, err error) {
	trimmed := strings.TrimSpace(designSource)
	if !strings.ContainsAny(trimmed, "\n\r") {
		if info, statErr := os.Stat(trimmed); statErr == nil {
			modDir, copyErr := copyFilesystemDesign(buildDir, trimmed, info)
			return modDir, "", "", copyErr
		}
	}

	if hasTxtarMarkers(designSource) {
		ar := txtar.Parse([]byte(designSource))
		wroteDesignFile := false
		if strings.TrimSpace(string(ar.Comment)) != "" {
			normalized, normErr := normalizeDesignPackage(string(ar.Comment))
			if normErr != nil {
				return "", "", "", normErr
			}
			if writeErr := os.WriteFile(filepath.Join(buildDir, "design", "design_00.go"), []byte(normalized), 0o600); writeErr != nil {
				return "", "", "", writeErr
			}
			wroteDesignFile = true
		}
		for i, f := range ar.Files {
			clean := filepath.Clean(strings.TrimSpace(f.Name))
			if clean == "." || clean == "" || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
				return "", "", "", fmt.Errorf("invalid archive file path %q", f.Name)
			}
			if clean == "go.mod" {
				embeddedGoMod = string(f.Data)
				continue
			}
			if clean == "go.sum" {
				embeddedGoSum = string(f.Data)
				continue
			}
			if strings.HasSuffix(clean, "_test.go") {
				continue
			}
			if filepath.Dir(clean) == "." && strings.HasSuffix(clean, ".go") {
				clean = filepath.Join("design", clean)
			}
			if !strings.HasSuffix(clean, ".go") && filepath.Base(clean) != "go.mod" && filepath.Base(clean) != "go.sum" {
				continue
			}
			content := string(f.Data)
			if filepath.Dir(clean) == "design" && strings.HasSuffix(clean, ".go") {
				normalized, normErr := normalizeDesignPackage(content)
				if normErr != nil {
					return "", "", "", fmt.Errorf("%s: %w", f.Name, normErr)
				}
				content = normalized
				wroteDesignFile = true
			}
			dst := filepath.Join(buildDir, clean)
			if mkErr := os.MkdirAll(filepath.Dir(dst), 0o750); mkErr != nil {
				return "", "", "", mkErr
			}
			if writeErr := os.WriteFile(dst, []byte(content), 0o600); writeErr != nil {
				return "", "", "", fmt.Errorf("write %s (%d): %w", clean, i, writeErr)
			}
		}
		if !wroteDesignFile {
			return "", "", "", errors.New("multi-file design archive contains no design/*.go files")
		}
		return "", embeddedGoMod, embeddedGoSum, nil
	}

	chunks := splitConcatenatedGoFiles(designSource)
	if len(chunks) > 1 {
		for i, chunk := range chunks {
			normalized, normErr := normalizeDesignPackage(chunk)
			if normErr != nil {
				return "", "", "", fmt.Errorf("file %d: %w", i+1, normErr)
			}
			name := fmt.Sprintf("design_%02d.go", i+1)
			if writeErr := os.WriteFile(filepath.Join(buildDir, "design", name), []byte(normalized), 0o600); writeErr != nil {
				return "", "", "", writeErr
			}
		}
		return "", "", "", nil
	}

	normalizedSrc, err := normalizeDesignPackage(designSource)
	if err != nil {
		return "", "", "", err
	}
	if err := os.WriteFile(filepath.Join(buildDir, "design", "design.go"), []byte(normalizedSrc), 0o600); err != nil {
		return "", "", "", fmt.Errorf("write design.go: %w", err)
	}
	return "", "", "", nil
}

// copyFilesystemDesign copies a design file, design directory, or module root
// directory from disk into buildDir, preserving multi-file design packages and
// sibling Go packages within the enclosing module.
func copyFilesystemDesign(buildDir, targetPath string, info os.FileInfo) (string, error) {
	absPath, err := filepath.Abs(targetPath)
	if err != nil {
		return "", err
	}
	var designDir, modDir, singleFile string
	if !info.IsDir() {
		parentDir := filepath.Dir(absPath)
		modDir = findEnclosingModuleDir(parentDir)
		if filepath.Base(parentDir) == "design" {
			designDir = parentDir
		} else {
			singleFile = absPath
		}
	} else {
		if sub, statErr := os.Stat(filepath.Join(absPath, "design")); statErr == nil && sub.IsDir() {
			designDir = filepath.Join(absPath, "design")
			modDir = findEnclosingModuleDir(absPath)
			if modDir == "" {
				modDir = absPath
			}
		} else {
			designDir = absPath
			modDir = findEnclosingModuleDir(designDir)
		}
	}

	if singleFile != "" {
		raw, readErr := os.ReadFile(singleFile)
		if readErr != nil {
			return "", readErr
		}
		normalized, normErr := normalizeDesignPackage(string(raw))
		if normErr != nil {
			return "", fmt.Errorf("%s: %w", filepath.Base(singleFile), normErr)
		}
		if writeErr := os.WriteFile(filepath.Join(buildDir, "design", filepath.Base(singleFile)), []byte(normalized), 0o600); writeErr != nil {
			return "", writeErr
		}
	} else {
		entries, readDirErr := os.ReadDir(designDir)
		if readDirErr != nil {
			return "", readDirErr
		}
		wroteDesign := false
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			raw, readErr := os.ReadFile(filepath.Join(designDir, entry.Name()))
			if readErr != nil {
				return "", readErr
			}
			normalized, normErr := normalizeDesignPackage(string(raw))
			if normErr != nil {
				return "", fmt.Errorf("%s: %w", entry.Name(), normErr)
			}
			if writeErr := os.WriteFile(filepath.Join(buildDir, "design", entry.Name()), []byte(normalized), 0o600); writeErr != nil {
				return "", writeErr
			}
			wroteDesign = true
		}
		if !wroteDesign {
			return "", fmt.Errorf("no Go design files found in %q", designDir)
		}
	}

	if modDir != "" && modDir != designDir {
		if copyErr := copyModuleGoTree(modDir, buildDir, designDir, singleFile); copyErr != nil {
			return "", copyErr
		}
	}
	return modDir, nil
}

// copyModuleGoTree copies non-test Go packages from srcDir into dstDir,
// skipping skipDir and skipFile.
func copyModuleGoTree(srcDir, dstDir, skipDir, skipFile string) error {
	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, walkInErr error) error {
		if walkInErr != nil {
			return walkInErr
		}
		if skipDir != "" && path == skipDir {
			return filepath.SkipDir
		}
		if skipFile != "" && path == skipFile {
			return nil
		}
		rel, relErr := filepath.Rel(srcDir, path)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			base := d.Name()
			if strings.HasPrefix(base, ".") || base == "vendor" || base == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Dir(rel) == "." || !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		dst := filepath.Join(dstDir, rel)
		if mkErr := os.MkdirAll(filepath.Dir(dst), 0o750); mkErr != nil {
			return mkErr
		}
		return os.WriteFile(dst, raw, 0o600)
	})
}

// findEnclosingModuleDir walks upward from startDir looking for a directory
// containing go.mod, stopping before escaping the filesystem root.
func findEnclosingModuleDir(startDir string) string {
	dir := startDir
	for {
		if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// hasTxtarMarkers reports whether src uses txtar `-- filename --` section headers.
func hasTxtarMarkers(src string) bool {
	trimmed := strings.TrimLeft(src, " \t\r\n")
	if strings.HasPrefix(trimmed, "-- ") && strings.Contains(trimmed, " --\n") {
		return true
	}
	return strings.Contains(src, "\n-- ") && strings.Contains(src, " --\n")
}

// splitConcatenatedGoFiles splits a string containing multiple top-level
// `package <name>` clauses into individual Go file strings using Go's lexical
// scanner so string literals and comments containing `package ` are never split.
func splitConcatenatedGoFiles(src string) []string {
	if _, err := parser.ParseFile(token.NewFileSet(), "design.go", src, 0); err == nil {
		return []string{src}
	}
	fset := token.NewFileSet()
	file := fset.AddFile("concatenated.go", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), func(_ token.Position, _ string) {}, 0)

	var pkgOffsets []int
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.PACKAGE {
			pkgOffsets = append(pkgOffsets, file.Offset(pos))
		}
	}
	if len(pkgOffsets) <= 1 {
		return []string{src}
	}
	chunks := make([]string, 0, len(pkgOffsets))
	for i, start := range pkgOffsets {
		if i == 0 {
			start = 0
		}
		end := len(src)
		if i+1 < len(pkgOffsets) {
			end = pkgOffsets[i+1]
		}
		chunks = append(chunks, src[start:end])
	}
	return chunks
}

// isCoreHarnessModule reports whether modPath is one of the core Goa/Goa-AI/Model
// modules imported directly by the standalone evaluator harness.
func isCoreHarnessModule(modPath string) bool {
	return modPath == "goa.design/goa/v3" ||
		modPath == "goa.design/goa-ai" ||
		modPath == "goa.design/model"
}

// copyReplacedModuleDir copies a local Go module directory into dstDir so it
// is readable inside candidate.Build's OS sandbox.
func copyReplacedModuleDir(srcDir, dstDir string) error {
	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, walkInErr error) error {
		if walkInErr != nil {
			return walkInErr
		}
		rel, relErr := filepath.Rel(srcDir, path)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			base := d.Name()
			if strings.HasPrefix(base, ".") || base == "vendor" || base == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") && d.Name() != "go.mod" && d.Name() != "go.sum" {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		dst := filepath.Join(dstDir, rel)
		if mkErr := os.MkdirAll(filepath.Dir(dst), 0o750); mkErr != nil {
			return mkErr
		}
		return os.WriteFile(dst, raw, 0o600)
	})
}

// prepareBuildGoMod writes buildDir/go.mod and buildDir/go.sum by combining
// Scantle's evaluator dependencies with the target module path and any
// resolvable require/replace directives from goMod or targetModDir.
func prepareBuildGoMod(buildDir, modRoot, baseModPath, targetModDir, goMod, embeddedGoSum, modCache string) (string, error) {
	baseModBytes, err := os.ReadFile(filepath.Join(modRoot, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	baseSumBytes, err := os.ReadFile(filepath.Join(modRoot, "go.sum"))
	if err != nil {
		return "", fmt.Errorf("read go.sum: %w", err)
	}
	if strings.TrimSpace(embeddedGoSum) != "" {
		baseSumBytes = append(append(baseSumBytes, '\n'), []byte(embeddedGoSum)...)
	}
	baseFile, err := modfile.Parse("go.mod", baseModBytes, nil)
	if err != nil {
		return "", fmt.Errorf("parse base go.mod: %w", err)
	}
	for _, rep := range baseFile.Replace {
		if rep != nil && rep.New.Version == "" && !filepath.IsAbs(rep.New.Path) {
			absTarget := filepath.Join(modRoot, rep.New.Path)
			if err := baseFile.AddReplace(rep.Old.Path, rep.Old.Version, absTarget, ""); err != nil {
				return "", err
			}
		}
	}

	effectiveModPath := baseModPath
	if strings.TrimSpace(goMod) == "" && targetModDir != "" {
		if rawTargetMod, readErr := os.ReadFile(filepath.Join(targetModDir, "go.mod")); readErr == nil {
			goMod = string(rawTargetMod)
		}
		if rawTargetSum, readErr := os.ReadFile(filepath.Join(targetModDir, "go.sum")); readErr == nil {
			baseSumBytes = append(append(baseSumBytes, '\n'), rawTargetSum...)
		}
	}

	if strings.TrimSpace(goMod) != "" {
		targetFile, parseErr := modfile.Parse("go.mod", []byte(goMod), nil)
		if parseErr != nil {
			return "", fmt.Errorf("parse target go.mod: %w", parseErr)
		}
		if targetFile.Module != nil && strings.TrimSpace(targetFile.Module.Mod.Path) != "" {
			effectiveModPath = strings.TrimSpace(targetFile.Module.Mod.Path)
			if err := baseFile.AddModuleStmt(effectiveModPath); err != nil {
				return "", fmt.Errorf("set module path: %w", err)
			}
		}
		for idx, rep := range targetFile.Replace {
			if rep == nil {
				continue
			}
			if rep.New.Version == "" {
				if isCoreHarnessModule(rep.Old.Path) {
					continue
				}
				var resolvedDir string
				inBuildCandidate := filepath.Join(buildDir, rep.New.Path)
				if !filepath.IsAbs(rep.New.Path) {
					if info, statErr := os.Stat(filepath.Join(inBuildCandidate, "go.mod")); statErr == nil && !info.IsDir() {
						resolvedDir = inBuildCandidate
					} else if targetModDir != "" {
						cand := filepath.Join(targetModDir, rep.New.Path)
						if info, statErr := os.Stat(filepath.Join(cand, "go.mod")); statErr == nil && !info.IsDir() {
							resolvedDir = cand
						}
					}
				} else if info, statErr := os.Stat(filepath.Join(rep.New.Path, "go.mod")); statErr == nil && !info.IsDir() {
					resolvedDir = rep.New.Path
				}
				if resolvedDir == "" {
					continue
				}
				relToBuild, relErr := filepath.Rel(buildDir, resolvedDir)
				if relErr != nil || strings.HasPrefix(relToBuild, "..") {
					relDir := filepath.Join("_replaces", fmt.Sprintf("rep_%02d", idx))
					dstDir := filepath.Join(buildDir, relDir)
					if copyErr := copyReplacedModuleDir(resolvedDir, dstDir); copyErr != nil {
						return "", copyErr
					}
					relToBuild = "./" + filepath.ToSlash(relDir)
				} else if !strings.HasPrefix(relToBuild, ".") {
					relToBuild = "./" + filepath.ToSlash(relToBuild)
				}
				if err := baseFile.AddReplace(rep.Old.Path, rep.Old.Version, relToBuild, ""); err != nil {
					return "", err
				}
			} else if sumLines, ok := cachedModuleSumLines(modCache, rep.New.Path, rep.New.Version); ok {
				if err := baseFile.AddReplace(rep.Old.Path, rep.Old.Version, rep.New.Path, rep.New.Version); err != nil {
					return "", err
				}
				baseSumBytes = append(baseSumBytes, []byte(sumLines)...)
			}
		}
		for _, req := range targetFile.Require {
			if req == nil {
				continue
			}
			if sumLines, ok := cachedModuleSumLines(modCache, req.Mod.Path, req.Mod.Version); ok {
				if err := baseFile.AddRequire(req.Mod.Path, req.Mod.Version); err != nil {
					return "", err
				}
				baseSumBytes = append(baseSumBytes, []byte(sumLines)...)
			}
		}
	}

	formattedMod, err := baseFile.Format()
	if err != nil {
		return "", fmt.Errorf("format build go.mod: %w", err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "go.mod"), formattedMod, 0o600); err != nil {
		return "", fmt.Errorf("write build go.mod: %w", err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "go.sum"), baseSumBytes, 0o600); err != nil {
		return "", fmt.Errorf("write build go.sum: %w", err)
	}
	return effectiveModPath, nil
}

// cachedModuleSumLines verifies whether modPath@version is present in modCache
// and returns its go.sum lines (`h1:...` for both archive and go.mod).
func cachedModuleSumLines(modCache, modPath, version string) (string, bool) {
	escapedPath, err := module.EscapePath(modPath)
	if err != nil {
		return "", false
	}
	escapedVer, err := module.EscapeVersion(version)
	if err != nil {
		return "", false
	}
	dlDir := filepath.Join(modCache, "cache", "download", escapedPath, "@v")
	zipHashBytes, err := os.ReadFile(filepath.Join(dlDir, escapedVer+".ziphash"))
	if err != nil {
		return "", false
	}
	modBytes, err := os.ReadFile(filepath.Join(dlDir, escapedVer+".mod"))
	if err != nil {
		return "", false
	}
	modHash, err := dirhash.Hash1([]string{"go.mod"}, func(string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(modBytes)), nil
	})
	if err != nil {
		return "", false
	}
	zipHash := strings.TrimSpace(string(zipHashBytes))
	return fmt.Sprintf("\n%s %s %s\n%s %s/go.mod %s\n", modPath, version, zipHash, modPath, version, modHash), true
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

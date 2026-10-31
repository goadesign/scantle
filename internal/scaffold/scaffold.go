// Package scaffold creates a new Surla starter project for `surla init`: a
// Goa / Goa-AI service whose helper agent prices items through a tool, plus a
// starter evaluator suite that `surla verify` runs against it.
//
// Init builds the whole project in a staging directory next to the target,
// running `go mod tidy` and `goa gen` in each module, and renames the staging
// directory to the target only when every step succeeded. A failed or
// canceled step removes the staging directory, so the target is never left
// half-written.
package scaffold

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"text/template"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/goadesign/surla"
	"github.com/goadesign/surla/internal/procgroup"
)

type (
	// Options describes the project Init creates.
	Options struct {
		// Dir is the directory to create. It must not exist or must be empty.
		Dir string
		// Module is the candidate's Go module path, such as
		// example.com/shop. Its last element names the service.
		Module string
		// SurlaVersion is the exact github.com/goadesign/surla version the
		// evaluator module requires, such as
		// v0.0.0-20261031211028-8ec6e0997e6b.
		SurlaVersion string
	}

	// Runner runs one command in dir and returns its combined output. Init
	// uses RunCommand; tests substitute their own.
	Runner func(ctx context.Context, dir string, args ...string) (output []byte, err error)

	// data is what the templates are rendered with.
	data struct {
		// Name is the service name, the last element of Module.
		Name string
		// Module is the candidate's module path.
		Module string
		// SurlaVersion is the evaluator's required Surla version.
		SurlaVersion string
		// GoVersion is the go directive of Surla's own go.mod.
		GoVersion string
		// GoaVersion, GoaAIVersion and ModelVersion are the versions
		// Surla's own go.mod requires.
		GoaVersion, GoaAIVersion, ModelVersion string
	}

	// moduleStep is one module of the project and the files it is built
	// from.
	moduleStep struct {
		// dir is the module's directory relative to the project root.
		dir string
		// templates is the template directory holding its files.
		templates string
		// path is the module path.
		path string
		// requireSurla is set for the evaluator module, which pins Surla.
		requireSurla bool
	}

	// StepError reports the command that failed while creating a project.
	StepError struct {
		// Args is the command line.
		Args []string
		// Dir is the directory it ran in, inside the staging directory.
		Dir string
		// Output is the end of its combined output.
		Output string
		// Err is the command's error.
		Err error
	}

	// stuckError reports a command whose processes did not all exit.
	stuckError struct {
		// err is procgroup.Run's stop error.
		err error
	}
)

const (
	// surlaModule is Surla's module path.
	surlaModule = "github.com/goadesign/surla"
	// stopGrace is how long a canceled command's process group gets to exit
	// after SIGTERM.
	stopGrace = 5 * time.Second
	// stopTimeout bounds the wait for a command's process group to empty.
	stopTimeout = 10 * time.Second
	// maxOutputExcerpt bounds the command output a StepError quotes.
	maxOutputExcerpt = 4 << 10
)

var (
	// templatesFS holds the project files. Files end in .tmpl so the Go
	// tooling ignores them here; "_name_" in a path is the service name, and
	// "gitignore.tmpl" becomes ".gitignore".
	//
	//go:embed all:templates
	templatesFS embed.FS

	// serviceName is the form a service name must have: lowercase letters
	// and digits starting with a letter, so it is the same Go package name
	// Goa generates.
	serviceName = regexp.MustCompile(`^[a-z][a-z0-9]{0,30}$`)

	// goKeywords cannot be service names because the name becomes a Go
	// identifier.
	goKeywords = map[string]bool{
		"break": true, "case": true, "chan": true, "const": true, "continue": true, "default": true,
		"defer": true, "else": true, "fallthrough": true, "for": true, "func": true, "go": true, "goto": true,
		"if": true, "import": true, "interface": true, "map": true, "package": true, "range": true,
		"return": true, "select": true, "struct": true, "switch": true, "type": true, "var": true,
	}
)

// Init creates the project described by o, running every command with run.
// It returns the service name. On error the target directory is unchanged.
func Init(ctx context.Context, o Options, run Runner) (string, error) {
	d, err := newData(o)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(o.Dir)
	if err != nil {
		return "", err
	}
	if err := checkEmpty(target); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(filepath.Dir(target), filepath.Base(target)+".surla-init-*")
	if err != nil {
		return "", fmt.Errorf("create staging directory: %w", err)
	}
	if err := build(ctx, staging, d, run); err != nil {
		var stuck *stuckError
		if errors.As(err, &stuck) {
			return "", fmt.Errorf("%w; the staging directory %s is kept because processes may still use it", err, staging)
		}
		return "", errors.Join(err, os.RemoveAll(staging))
	}
	if err := checkEmpty(target); err != nil {
		return "", errors.Join(err, os.RemoveAll(staging))
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", errors.Join(fmt.Errorf("replace empty %s: %w", target, err), os.RemoveAll(staging))
	}
	if err := os.Rename(staging, target); err != nil {
		return "", errors.Join(fmt.Errorf("move project into %s: %w", target, err), os.RemoveAll(staging))
	}
	return d.Name, nil
}

// RunCommand runs args in dir as its own process group with GOWORK=off, so a
// go.work file around dir cannot change the new modules, and returns the
// combined output. When ctx ends the group gets SIGTERM, then SIGKILL; a group
// that does not exit is reported as a stuck error so Init keeps the staging
// directory its processes may still use.
func RunCommand(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr, stopErr := procgroup.Run(cmd, syscall.SIGTERM, stopGrace, stopTimeout)
	if stopErr != nil {
		return out.Bytes(), &stuckError{err: stopErr}
	}
	return out.Bytes(), runErr
}

// ValidVersion reports whether v is an exact module version: a canonical
// semantic version such as v1.2.3, v1.2.3-pre or a pseudo-version, not a
// query such as latest or a branch name, and without build metadata such as
// +dirty.
func ValidVersion(v string) bool {
	return semver.IsValid(v) && semver.Canonical(v) == v
}

// Error describes the failed command and the end of its output.
func (e *StepError) Error() string {
	return fmt.Sprintf("%s (in %s) failed: %v\n%s", strings.Join(e.Args, " "), e.Dir, e.Err, e.Output)
}

// Unwrap returns the command's error.
func (e *StepError) Unwrap() error {
	return e.Err
}

// Error describes the processes left running.
func (e *stuckError) Error() string {
	return "command processes did not exit: " + e.err.Error()
}

// Unwrap returns procgroup.Run's stop error.
func (e *stuckError) Unwrap() error {
	return e.err
}

// newData validates o and returns the template data, with the Go, Goa,
// Goa-AI and Model versions read from Surla's own go.mod.
func newData(o Options) (data, error) {
	if err := module.CheckPath(o.Module); err != nil {
		return data{}, fmt.Errorf("-module %q: %w", o.Module, err)
	}
	name := path.Base(o.Module)
	if !serviceName.MatchString(name) || goKeywords[name] {
		return data{}, fmt.Errorf("-module %q must end in a service name of lowercase letters and digits starting with a letter, such as example.com/shop", o.Module)
	}
	if !ValidVersion(o.SurlaVersion) {
		return data{}, fmt.Errorf("Surla version %q is not an exact module version such as v0.0.0-20261031211028-8ec6e0997e6b", o.SurlaVersion)
	}
	mod, err := modfile.Parse("go.mod", []byte(surla.GoMod()), nil)
	if err != nil {
		return data{}, fmt.Errorf("parse Surla's go.mod: %w", err)
	}
	d := data{Name: name, Module: o.Module, SurlaVersion: o.SurlaVersion, GoVersion: mod.Go.Version}
	for _, req := range []struct {
		path string
		dst  *string
	}{
		{"goa.design/goa/v3", &d.GoaVersion},
		{"goa.design/goa-ai", &d.GoaAIVersion},
		{"goa.design/model", &d.ModelVersion},
	} {
		for _, r := range mod.Require {
			if r.Mod.Path == req.path {
				*req.dst = r.Mod.Version
			}
		}
		if *req.dst == "" {
			return data{}, fmt.Errorf("Surla's go.mod does not require %s", req.path)
		}
	}
	return d, nil
}

// checkEmpty returns an error unless dir is missing or an empty directory.
func checkEmpty(dir string) error {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("read %s: %w", dir, err)
	case len(entries) > 0:
		return fmt.Errorf("%s is not empty; surla init only creates new projects", dir)
	}
	return nil
}

// build creates both modules under root. For each module it writes go.mod and
// design/, resolves the design's dependencies, generates code with goa gen,
// writes the remaining files, and tidies again. The evaluator module then
// pins the exact Surla version before its final tidy, so tidy cannot pick a
// different one.
func build(ctx context.Context, root string, d data, run Runner) error {
	for _, m := range []moduleStep{
		{dir: ".", templates: "templates/candidate", path: d.Module},
		{dir: "eval", templates: "templates/eval", path: d.Module + "/eval", requireSurla: true},
	} {
		dir := filepath.Join(root, m.dir)
		if err := render(dir, m.templates, d, isDesignFile); err != nil {
			return err
		}
		steps := [][]string{
			{"go", "mod", "tidy"},
			{"go", "run", "goa.design/goa/v3/cmd/goa", "gen", m.path + "/design"},
		}
		if err := runAll(ctx, dir, steps, run); err != nil {
			return err
		}
		if err := render(dir, m.templates, d, func(p string) bool { return !isDesignFile(p) }); err != nil {
			return err
		}
		steps = nil
		if m.requireSurla {
			steps = append(steps, []string{"go", "get", surlaModule + "@" + d.SurlaVersion})
		}
		steps = append(steps, []string{"go", "mod", "tidy"}, []string{"go", "vet", "./..."})
		if err := runAll(ctx, dir, steps, run); err != nil {
			return err
		}
	}
	return nil
}

// runAll runs each command in dir in order and stops at the first failure.
func runAll(ctx context.Context, dir string, steps [][]string, run Runner) error {
	for _, args := range steps {
		out, err := run(ctx, dir, args...)
		if err != nil {
			var stuck *stuckError
			if errors.As(err, &stuck) {
				return err
			}
			return &StepError{Args: args, Dir: dir, Output: excerpt(out), Err: err}
		}
	}
	return nil
}

// isDesignFile reports whether a template path is written before goa gen:
// go.mod and the design package.
func isDesignFile(p string) bool {
	return p == "go.mod.tmpl" || strings.HasPrefix(p, "design/")
}

// render writes every template under root in templatesFS for which keep
// reports true into dir, with "_name_" in paths replaced by the service name.
func render(dir, root string, d data, keep func(string) bool) error {
	return fs.WalkDir(templatesFS, root, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(p, root+"/")
		if !keep(rel) {
			return nil
		}
		raw, err := templatesFS.ReadFile(p)
		if err != nil {
			return err
		}
		t, err := template.New(rel).Delims("[[", "]]").Option("missingkey=error").Parse(string(raw))
		if err != nil {
			return fmt.Errorf("parse template %s: %w", rel, err)
		}
		var b bytes.Buffer
		if err := t.Execute(&b, d); err != nil {
			return fmt.Errorf("render template %s: %w", rel, err)
		}
		out := strings.ReplaceAll(strings.TrimSuffix(rel, ".tmpl"), "_name_", d.Name)
		if out == "gitignore" {
			out = ".gitignore"
		}
		dst := filepath.Join(dir, filepath.FromSlash(out))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, b.Bytes(), 0o644)
	})
}

// excerpt returns at most the last maxOutputExcerpt bytes of out.
func excerpt(out []byte) string {
	if len(out) <= maxOutputExcerpt {
		return string(out)
	}
	return "...\n" + string(out[len(out)-maxOutputExcerpt:])
}

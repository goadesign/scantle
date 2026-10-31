// Package testmodule builds real Go module snapshots of Goa designs for tests
// of the design evaluator and its callers.
package testmodule

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

// Snapshot returns the files of a tidy module named example.com/design whose
// design/ package holds design (as design/design.go), with Module's
// requirements.
func Snapshot(t testing.TB, goaVersion, design string) map[string]string {
	t.Helper()
	return Module(t, "example.com/design", goaVersion, map[string]string{"design/design.go": design})
}

// Module returns the files of a tidy module at modulePath holding files. It
// requires goa.design/goa/v3 at goaVersion (Surla's own version when empty)
// and goa.design/goa-ai and goa.design/model at Surla's versions; go mod
// tidy drops the ones the files do not import and completes go.mod and go.sum.
func Module(t testing.TB, modulePath, goaVersion string, files map[string]string) map[string]string {
	t.Helper()
	if goaVersion == "" {
		goaVersion = SurlaVersion(t, "goa.design/goa/v3")
	}
	out := maps.Clone(files)
	out["go.mod"] = "module " + modulePath + "\n\ngo 1.24\n\nrequire (\n" +
		"\tgoa.design/goa/v3 " + goaVersion + "\n" +
		"\tgoa.design/goa-ai " + SurlaVersion(t, "goa.design/goa-ai") + "\n" +
		"\tgoa.design/model " + SurlaVersion(t, "goa.design/model") + "\n)\n"
	return Tidy(t, out)
}

// Tidy writes files, which must include go.mod, to a new module directory,
// runs go mod tidy, and returns the files with the resulting go.mod and
// go.sum.
func Tidy(t testing.TB, files map[string]string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	out := maps.Clone(files)
	for name, content := range out {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	tidy.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local")
	if b, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy: %v\n%s", err, b)
	}
	for _, name := range []string{"go.mod", "go.sum"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		out[name] = string(b)
	}
	return out
}

// SurlaVersion returns the version Surla's own go.mod requires of path.
func SurlaVersion(t testing.TB, path string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	mod, err := modfile.Parse("go.mod", raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range mod.Require {
		if r.Mod.Path == path {
			return r.Mod.Version
		}
	}
	t.Fatalf("Surla's go.mod does not require %s", path)
	return ""
}

// WithFile returns a copy of files with name set to content.
func WithFile(files map[string]string, name, content string) map[string]string {
	out := maps.Clone(files)
	out[name] = content
	return out
}

// Replace returns files with old replaced by new in the named file.
func Replace(t testing.TB, files map[string]string, name, old, replacement string) map[string]string {
	t.Helper()
	if !strings.Contains(files[name], old) {
		t.Fatalf("%s does not contain %q", name, old)
	}
	return WithFile(files, name, strings.Replace(files[name], old, replacement, 1))
}

package scaffold

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// published is a Surla version that exists on the module proxy.
const published = "v0.0.0-20261031211028-8ec6e0997e6b"

func TestValidVersionAcceptsOnlyExactVersions(t *testing.T) {
	for v, want := range map[string]bool{
		"v1.2.3":     true,
		"v1.2.3-pre": true,
		published:    true,
		"v0.0.0-20261031211028-8ec6e0997e6b+dirty": false,
		"latest":  false,
		"main":    false,
		"8ec6e09": false,
		"(devel)": false,
		"":        false,
		"v1.2":    false,
		"1.2.3":   false,
	} {
		assert.Equal(t, want, ValidVersion(v), v)
	}
}

func TestNewDataValidatesTheModuleAndReadsSurlasVersions(t *testing.T) {
	d, err := newData(Options{Module: "example.com/shop", SurlaVersion: published})
	require.NoError(t, err)
	assert.Equal(t, "shop", d.Name)
	assert.True(t, strings.HasPrefix(d.GoaVersion, "v3."), d.GoaVersion)
	assert.NotEmpty(t, d.GoaAIVersion)
	assert.NotEmpty(t, d.ModelVersion)
	assert.NotEmpty(t, d.GoVersion)

	for _, tc := range []struct {
		module, version, wantErr string
	}{
		{"example.com/my-shop", published, "must end in a service name"},
		{"example.com/Shop", published, "must end in a service name"},
		{"example.com/func", published, "must end in a service name"},
		{"not a path", published, "-module"},
		{"example.com/shop", "latest", "not an exact module version"},
	} {
		_, err := newData(Options{Module: tc.module, SurlaVersion: tc.version})
		assert.ErrorContains(t, err, tc.wantErr, tc.module+" "+tc.version)
	}
}

func TestInitRefusesANonEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("mine"), 0o600))
	_, err := Init(t.Context(), Options{Dir: dir, Module: "example.com/shop", SurlaVersion: published}, failRunner(t))
	require.ErrorContains(t, err, "is not empty")
	assertOnly(t, dir, "keep.txt")
}

func TestInitLeavesNothingWhenAStepFails(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "shop")
	var ran [][]string
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		ran = append(ran, args)
		if args[1] == "run" {
			return []byte("design/design.go:3: boom"), errors.New("exit status 1")
		}
		return nil, nil
	}
	_, err := Init(t.Context(), Options{Dir: dir, Module: "example.com/shop", SurlaVersion: published}, run)
	var step *StepError
	require.ErrorAs(t, err, &step)
	assert.Equal(t, []string{"go", "run", "goa.design/goa/v3/cmd/goa", "gen", "example.com/shop/design"}, step.Args)
	assert.Contains(t, err.Error(), "design/design.go:3: boom")
	assert.Equal(t, [][]string{{"go", "mod", "tidy"}, step.Args}, ran)
	assertOnly(t, parent)
}

func TestInitStopsAndCleansUpWhenCanceled(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "shop")
	ctx, cancel := context.WithCancel(t.Context())
	run := func(ctx context.Context, dir string, _ ...string) ([]byte, error) {
		time.AfterFunc(100*time.Millisecond, cancel)
		return RunCommand(ctx, dir, "sleep", "30")
	}
	start := time.Now()
	_, err := Init(ctx, Options{Dir: dir, Module: "example.com/shop", SurlaVersion: published}, run)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 10*time.Second, "the canceled command's process group is stopped")
	assertOnly(t, parent)
}

func TestInitWritesTheProjectWithTheServiceName(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "bakery")
	var ran []string
	run := func(_ context.Context, dir string, args ...string) ([]byte, error) {
		ran = append(ran, strings.Join(args, " "))
		return nil, nil
	}
	name, err := Init(t.Context(), Options{Dir: dir, Module: "example.com/bakery", SurlaVersion: published}, run)
	require.NoError(t, err)
	assert.Equal(t, "bakery", name)
	assert.Equal(t, []string{
		"go mod tidy", "go run goa.design/goa/v3/cmd/goa gen example.com/bakery/design", "go mod tidy", "go vet ./...",
		"go mod tidy", "go run goa.design/goa/v3/cmd/goa gen example.com/bakery/eval/design",
		"go get github.com/goadesign/surla@" + published, "go mod tidy", "go vet ./...",
	}, ran)
	for _, f := range []string{
		"go.mod", ".gitignore", "README.md", "design/design.go", "design/model.go",
		"internal/helper/planner.go", "internal/service/service.go", "cmd/bakery/main.go",
		"eval/go.mod", "eval/design/design.go", "eval/scripted/scripted.go",
		"eval/acceptance/acceptance.go", "eval/cmd/bakery-evals/main.go",
	} {
		assert.FileExists(t, filepath.Join(dir, f))
	}
	mod, err := os.ReadFile(filepath.Join(dir, "eval", "go.mod"))
	require.NoError(t, err)
	assert.Contains(t, string(mod), "module example.com/bakery/eval")
	assert.NotContains(t, string(mod), "replace")
	main, err := os.ReadFile(filepath.Join(dir, "cmd", "bakery", "main.go"))
	require.NoError(t, err)
	assert.Contains(t, string(main), `genbakery "example.com/bakery/gen/bakery"`)
	assert.NotContains(t, string(main), "[[")
	assertOnly(t, parent, "bakery")
}

// failRunner fails the test if Init runs any command.
func failRunner(t *testing.T) Runner {
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		t.Fatalf("unexpected command %v", args)
		return nil, nil
	}
}

// assertOnly checks that dir holds exactly the named entries.
func assertOnly(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	assert.ElementsMatch(t, names, got)
}

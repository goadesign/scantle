package designeval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goadesign/surla/internal/testmodule"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minimalDesign is a valid Goa design used by the boundary tests.
const minimalDesign = `package design

import . "goa.design/goa/v3/dsl"

var _ = Service("orders", func() {
	Method("get_order", func() {
		Payload(String)
		Result(String)
	})
})
`

func TestValidatePathRejectsPathsOutsideTheModuleContract(t *testing.T) {
	for _, name := range []string{
		"", "/etc/passwd", "../x.go", "design/../../x.go", "./design/a.go", "design//a.go", `design\a.go`,
		"surla_designeval/main.go", "surla_designeval", "sub/go.mod", "sub/go.sum", "design/notes.txt", "design/",
	} {
		assert.Error(t, validatePath(name), "%q must be rejected", name)
	}
	for _, name := range []string{"go.mod", "go.sum", "design/design.go", "internal/types/types.go", "design/a_test.go"} {
		assert.NoError(t, validatePath(name), "%q is a valid snapshot path", name)
	}
}

func TestValidateSnapshotRequiresACompleteModule(t *testing.T) {
	valid := map[string]string{
		"go.mod":           "module example.com/design\n\ngo 1.24\n",
		"go.sum":           "",
		"design/design.go": minimalDesign,
	}
	_, err := validateSnapshot(valid)
	require.NoError(t, err)

	cases := map[string]map[string]string{
		"no go.mod":                  testmodule.WithFile(without(valid, "go.mod"), "design/x.go", "package design"),
		"no go.sum":                  without(valid, "go.sum"),
		"no design package":          testmodule.WithFile(without(valid, "design/design.go"), "other/a.go", "package other"),
		"only design tests":          testmodule.WithFile(without(valid, "design/design.go"), "design/a_test.go", "package design"),
		"no module path":             testmodule.WithFile(valid, "go.mod", "go 1.24\n"),
		"relative directory replace": testmodule.WithFile(valid, "go.mod", "module example.com/design\n\nreplace goa.design/goa/v3 => ../goa\n"),
		"absolute directory replace": testmodule.WithFile(valid, "go.mod", "module example.com/design\n\nreplace goa.design/goa/v3 => /src/goa\n"),
		"escaping path":              testmodule.WithFile(valid, "../outside.go", "package x"),
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := validateSnapshot(files)
			assert.Error(t, err)
		})
	}

	_, err = validateSnapshot(without(valid, "go.sum"))
	assert.ErrorContains(t, err, "run `go mod tidy` in the module", "a missing go.sum tells the user how to create it")

	versioned := testmodule.WithFile(valid, "go.mod", "module example.com/design\n\nreplace goa.design/goa/v3 => goa.design/goa/v3 v3.20.0\n")
	_, err = validateSnapshot(versioned)
	assert.NoError(t, err, "a versioned replacement is resolved like any requirement")
}

func TestStageSnapshotWritesExactBytesWithoutReadingHostPaths(t *testing.T) {
	dir := t.TempDir()
	// A design file whose content names a real host path is only content.
	files := map[string]string{
		"go.mod":           "module example.com/design\n",
		"design/design.go": "/etc/passwd",
	}
	require.NoError(t, stageSnapshot(dir, files))
	for name, want := range files {
		got, err := readFile(dir, name)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	assert.Error(t, stageSnapshot(dir, files), "staging never overwrites or follows an existing file")
}

func TestSnapshotDigestDistinguishesEveryFile(t *testing.T) {
	a := map[string]string{"a.go": "x", "b.go": "y"}
	assert.Equal(t, SnapshotDigest(a), SnapshotDigest(map[string]string{"b.go": "y", "a.go": "x"}))
	assert.NotEqual(t, SnapshotDigest(a), SnapshotDigest(map[string]string{"a.go": "xb.go", "": "y"}),
		"length prefixes keep path and content boundaries apart")
	assert.NotEqual(t, SnapshotDigest(a), SnapshotDigest(map[string]string{"a.go": "x", "b.go": "z"}))
}

// without returns a copy of files without name.
func without(files map[string]string, name string) map[string]string {
	out := map[string]string{}
	for k, v := range files {
		if k != name {
			out[k] = v
		}
	}
	return out
}

// readFile reads a staged snapshot file.
func readFile(dir, name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	return string(b), err
}

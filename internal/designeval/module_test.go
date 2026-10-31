package designeval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goadesign/surla/internal/testmodule"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvaluateTreatsArchiveLookingTextAsPlainGo(t *testing.T) {
	design := strings.Replace(minimalDesign, `Service("orders"`, "Service(\"orders\", func() {\n\tDescription(`first line\n-- example.go --\nlast line`)\n})\n\nvar _ = Service(\"orders2\"", 1)
	res, err := Evaluate(t.Context(), testmodule.Snapshot(t, "", design))
	require.NoError(t, err)
	assert.Contains(t, res.Declarations, "Service:orders2/Method:get_order")
}

func TestEvaluateRejectsAnIncompleteModule(t *testing.T) {
	files := testmodule.Snapshot(t, "", minimalDesign)
	files["go.sum"] = ""
	_, err := Evaluate(t.Context(), files)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "target module is incomplete or unresolvable")
}

func TestEvaluateRefusesToUpgradeTheTargetsGenerator(t *testing.T) {
	// The target requires only goa, at a version older than the evaluator's
	// goa-ai needs; building the evaluator against it would upgrade goa.
	old := "v3.21.0"
	files := testmodule.Tidy(t, map[string]string{
		"go.mod":           "module example.com/design\n\ngo 1.24\n\nrequire goa.design/goa/v3 " + old + "\n",
		"design/design.go": minimalDesign,
	})
	require.Contains(t, files["go.mod"], "goa.design/goa/v3 "+old, "tidy keeps the target's own goa version")
	_, err := Evaluate(t.Context(), files)
	require.Error(t, err, "goa %s is older than the evaluator needs, so evaluating it would upgrade goa", old)
	assert.Contains(t, err.Error(), "goa.design/goa/v3: target selects "+old)
}

func TestEvaluateUsesTheTargetsOwnGeneratorVersion(t *testing.T) {
	res, err := Evaluate(t.Context(), testmodule.Snapshot(t, "", minimalDesign))
	require.NoError(t, err)
	assert.Contains(t, res.Declarations, "Service:orders/Method:get_order")
}

func TestEvaluateUsesTheBuildToolchainToResolveModules(t *testing.T) {
	// Module resolution must run with the toolchain the sandboxed build
	// compiles with, never whatever go is first on PATH: a design may require
	// that toolchain's exact Go version while PATH holds an older go.
	tc, err := resolveToolchainEnv()
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(tc.goroot, "VERSION"))
	require.NoError(t, err)
	version := strings.TrimPrefix(strings.SplitN(string(raw), "\n", 2)[0], "go")
	files := testmodule.Snapshot(t, "", minimalDesign)
	files["go.mod"] = strings.Replace(files["go.mod"], "go 1.24", "go "+version, 1)
	require.Contains(t, files["go.mod"], "go "+version)

	fakeBin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fakeBin, "go"),
		[]byte("#!/bin/sh\necho 'go on PATH must not be used' >&2\nexit 1\n"), 0o700))
	t.Setenv("PATH", fakeBin+":/usr/bin:/bin")

	res, err := Evaluate(t.Context(), files)
	require.NoError(t, err)
	assert.Contains(t, res.Declarations, "Service:orders/Method:get_order")
}

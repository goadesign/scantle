//go:build scaffold

package scaffold

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/eval"
)

// TestInitCreatesAProjectWhoseSuitePasses creates a real project with the
// published Surla version (it needs the network and, for the sandbox, macOS),
// builds the service and its evaluator suite, and runs the suite against the
// service: both scenarios must pass.
func TestInitCreatesAProjectWhoseSuitePasses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shop")
	name, err := Init(t.Context(), Options{Dir: dir, Module: "example.com/shop", SurlaVersion: published}, RunCommand)
	require.NoError(t, err)
	bin := t.TempDir()
	build := func(moduleDir, out, pkg string) {
		cmd := exec.CommandContext(t.Context(), "go", "build", "-o", filepath.Join(bin, out), pkg)
		cmd.Dir = moduleDir
		cmd.Env = append(cmd.Environ(), "GOWORK=off")
		b, err := cmd.CombinedOutput()
		require.NoError(t, err, string(b))
	}
	build(dir, name, "./cmd/"+name)
	build(filepath.Join(dir, "eval"), name+"-evals", "./cmd/"+name+"-evals")

	out, err := exec.CommandContext(t.Context(), filepath.Join(bin, name+"-evals"),
		"--artifact", name+"="+filepath.Join(bin, name)).Output()
	require.NoError(t, err, string(out))
	var report eval.Report
	require.NoError(t, json.Unmarshal(out, &report))
	assert.True(t, report.Passed)
	require.Len(t, report.Scenarios, 2)
	for _, s := range report.Scenarios {
		require.NotNil(t, s.Result, s.ID)
		assert.True(t, s.Passed, "%s: %+v", s.ID, s.Result.Checks)
	}
}

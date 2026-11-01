//go:build scaffold

package scaffold

import (
	"context"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestInitSendsRegisteredPromptReferences generates a real project and asks
// its service a question. The model request must carry the registered
// baseline text and version. The reserved service name also exercises Goa's
// different generated package name without changing the prompt ID.
func TestInitSendsRegisteredPromptReferences(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "errors")
	_, err := Init(ctx, Options{Dir: dir, Module: "example.com/errors", SurlaVersion: published}, RunCommand)
	require.NoError(t, err, "Init vets the generated candidate and evaluator modules")
	fixture, err := os.ReadFile("testdata/prompt_request_test.go.txt")
	require.NoError(t, err)
	source, err := format.Source([]byte(strings.NewReplacer(
		"PROJECT_MODULE", "example.com/errors",
		"PROJECT_PACKAGE", "errors_",
		"PROJECT_NAME", "errors",
	).Replace(string(fixture))))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "internal/service/prompt_request_test.go"), source, 0o600))
	cmd := exec.CommandContext(ctx, "go", "test", "-race", "-count=1", "-timeout=30s", "./internal/service")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "CI=true")
	// A generated-project test reads no input; nil stdin is /dev/null.
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "generated service did not send its registered prompt:\n%s", out)
}

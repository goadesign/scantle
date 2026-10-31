package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadVerificationConfigReadsTheOperatorsPlanStrictly(t *testing.T) {
	cfg, err := loadVerificationConfig("")
	require.NoError(t, err)
	assert.Nil(t, cfg, "no file means verification is not configured")

	complete := `{"runs_dir":"/srv/runs","report_dir":"/srv/reports","candidate_repo":"/srv/app.git",
		"candidate_module":".","binaries":{"app":"./cmd/app"},"suite_repo":"/srv/eval.git",
		"suite_commit":"` + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" + `","suite_module":".","suite_command":"./cmd/app-evals"}`
	write := func(t *testing.T, content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "verification.json")
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		return path
	}

	cfg, err = loadVerificationConfig(write(t, complete))
	require.NoError(t, err)
	assert.Equal(t, "/srv/runs", cfg.RunsDir)
	assert.Equal(t, "/srv/reports", cfg.Plan.ReportDir)
	assert.Equal(t, map[string]string{"app": "./cmd/app"}, cfg.Plan.Binaries)
	assert.Equal(t, "./cmd/app-evals", cfg.Plan.SuiteCommand)
	assert.Empty(t, cfg.Plan.CandidateCommit, "the candidate commit comes from each verify_change call")
	assert.Empty(t, cfg.Plan.WorkDir, "each run gets its own work directory")
	require.Len(t, cfg.Command, 1)

	for _, tc := range []struct {
		name, content, wantErr string
	}{
		{"unknown field", `{"runs_dir":"x","suite":"y"}`, "unknown field"},
		{"missing fields", `{"runs_dir":"/srv/runs"}`, "is missing"},
		{"trailing data", complete + `{}`, "data after the configuration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadVerificationConfig(write(t, tc.content))
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
	_, err = loadVerificationConfig(filepath.Join(t.TempDir(), "missing.json"))
	assert.ErrorContains(t, err, "read -verification-config")
}

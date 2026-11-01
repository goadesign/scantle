package designeval

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReleaseTempKeepsTheDirectoryOnlyForAStuckBuild checks the evaluation's
// temporary directory: removed, with a read-only artifact store inside, after
// a success or an ordinary failure; kept and named in the error when the
// build's processes may still use it.
func TestReleaseTempKeepsTheDirectoryOnlyForAStuckBuild(t *testing.T) {
	errBuild := errors.New("exit status 2")
	errStop := errors.New("process group 42 still has processes after 10s")
	cases := []struct {
		name string
		err  error
		kept bool
	}{
		{"success", nil, false},
		{"ordinary failure", errBuild, false},
		{"stuck build", errors.Join(&stuckBuildError{err: errStop}, errBuild), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tmpDir, storeDir := tempWithReadOnlyStore(t)

			err := releaseTemp(tmpDir, storeDir, c.err)

			if !c.kept {
				if c.err == nil {
					assert.NoError(t, err)
				} else {
					assert.ErrorIs(t, err, c.err)
					assert.EqualError(t, err, c.err.Error(), "removal succeeded, so nothing is added")
				}
				assert.NoDirExists(t, tmpDir)
				return
			}
			assert.DirExists(t, tmpDir)
			assert.ErrorContains(t, err, tmpDir)
			assert.ErrorIs(t, err, errStop, "the stop failure is kept")
			assert.ErrorIs(t, err, errBuild, "the build's own error is kept")
			var stuck *stuckBuildError
			assert.ErrorAs(t, err, &stuck)
		})
	}
}

// TestRunBuildReportsTheBuildsOwnResult checks that a build whose process
// group empties reports exactly the command's result, never a stuck build.
func TestRunBuildReportsTheBuildsOwnResult(t *testing.T) {
	assert.NoError(t, runBuild(exec.CommandContext(t.Context(), "true")))

	err := runBuild(exec.CommandContext(t.Context(), "sh", "-c", "exit 3"))
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 3, exit.ExitCode())
	var stuck *stuckBuildError
	assert.False(t, errors.As(err, &stuck))
}

// tempWithReadOnlyStore lays out a temporary directory like Evaluate's, with
// an artifact store whose directories are read-only as candidate.Pin leaves
// them, and returns both paths.
func tempWithReadOnlyStore(t *testing.T) (tmpDir, storeDir string) {
	t.Helper()
	tmpDir = filepath.Join(t.TempDir(), "surla-designeval-test")
	storeDir = filepath.Join(tmpDir, "store")
	pinned := filepath.Join(storeDir, "abc")
	require.NoError(t, os.MkdirAll(pinned, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(pinned, "designeval-bin"), []byte("bin"), 0o500))
	require.NoError(t, os.Chmod(pinned, 0o500))
	require.NoError(t, os.Chmod(storeDir, 0o500))
	t.Cleanup(func() {
		// A kept directory still has the read-only store.
		err := filepath.WalkDir(tmpDir, func(path string, d os.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return err
			}
			return os.Chmod(path, 0o700)
		})
		if !errors.Is(err, os.ErrNotExist) {
			assert.NoError(t, err)
		}
	})
	return tmpDir, storeDir
}

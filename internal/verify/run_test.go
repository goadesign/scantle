package verify

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunAcceptsOnlyFullCommitIDs(t *testing.T) {
	full := "4d77355e0c1b2a3f4d5e6f708192a3b4c5d6e7f8"
	cases := []struct {
		name      string
		candidate string
		suite     string
	}{
		{"candidate branch name", "main", full},
		{"suite branch name", full, "main"},
		{"symbolic ref", "HEAD", full},
		{"abbreviated ID", "4d77355", full},
		{"revision expression", full + "~1", full},
		{"uppercase ID", "4D77355E0C1B2A3F4D5E6F708192A3B4C5D6E7F8", full},
		{"length of neither object format", full + "0", full},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Run(t.Context(), Plan{CandidateCommit: c.candidate, SuiteCommit: c.suite, WorkDir: t.TempDir()})
			assert.ErrorContains(t, err, "is not a full commit ID")
		})
	}
}

func TestCheckoutRecordsTheExactCommitInEitherObjectFormat(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			repo := t.TempDir()
			git(t, repo, "init", "--quiet", "--object-format="+format)
			require.NoError(t, os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o600))
			git(t, repo, "add", "f")
			git(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "-m", "c")
			head := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
			require.Regexp(t, commitID, head)

			r := &runner{evidenceDir: t.TempDir()}
			require.NoError(t, r.checkout(t.Context(), "candidate", repo, head, filepath.Join(t.TempDir(), "clone")))
			other := strings.Repeat("0", len(head))
			assert.Error(t, r.checkout(t.Context(), "candidate", repo, other, filepath.Join(t.TempDir(), "clone")),
				"a well-formed ID that is not in the repository is refused")
		})
	}
}

// git runs one git command in dir and returns its output.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return string(out)
}

package verify

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Run(t.Context(), Plan{CandidateCommit: c.candidate, SuiteCommit: c.suite, WorkDir: t.TempDir()})
			assert.ErrorContains(t, err, "is not a full commit ID")
		})
	}
}

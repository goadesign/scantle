// Package goldenpath tests the embedded Golden Path playbook and CP1 architect prompt.
package goldenpath

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlaybookMarkdownAndArchitectSystemPromptEmbedSingleSourceOfTruth verifies
// that PlaybookMarkdown and ArchitectSystemPrompt both serve the embedded
// GOLDEN_PATH.md file without divergence.
func TestPlaybookMarkdownAndArchitectSystemPromptEmbedSingleSourceOfTruth(t *testing.T) {
	t.Parallel()

	md := PlaybookMarkdown()
	require.NotEmpty(t, strings.TrimSpace(md))

	prompt := ArchitectSystemPrompt()
	assert.Contains(t, prompt, "cp1_change_card_analysis")
	assert.True(t, strings.HasSuffix(prompt, md), "ArchitectSystemPrompt must embed PlaybookMarkdown verbatim")
}

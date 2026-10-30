// Package goldenpath embeds and serves the public Golden Path architecture
// playbook (`GOLDEN_PATH.md`) for building multi-service agentic platforms on
// the Goa Design suite (goa, goa-ai, pulse, clue, model, and chorus).
//
// Scantle serves the embedded playbook at the `scantle://golden-path` MCP
// resource and injects it into Checkpoint 1 (`CP1`) analysis so the architect
// agent decomposes requirements along clean trust, transport, inference, and
// state-ownership boundaries.
package goldenpath

import (
	_ "embed"
)

const (
	// ResourceURI is the canonical MCP resource URI serving the Golden Path playbook.
	ResourceURI = "scantle://golden-path"

	// cp1InstructionHeader precedes the embedded Golden Path Markdown in the
	// CP1 architect system prompt.
	cp1InstructionHeader = "Analyze the operator feature request against the Goa design source. Return structured JSON matching the cp1_change_card_analysis schema.\n\nApply the following Golden Path architectural playbook when identifying ambiguities, examples, and preserved behaviors:\n\n"
)

// playbookMarkdown holds the embedded contents of GOLDEN_PATH.md.
//
//go:embed GOLDEN_PATH.md
var playbookMarkdown string

// PlaybookMarkdown returns the embedded Golden Path architecture playbook
// Markdown served by the `scantle://golden-path` MCP resource.
func PlaybookMarkdown() string {
	return playbookMarkdown
}

// ArchitectSystemPrompt returns the CP1 system prompt combining the structured
// output instruction with the embedded Golden Path architectural playbook.
func ArchitectSystemPrompt() string {
	return cp1InstructionHeader + playbookMarkdown
}

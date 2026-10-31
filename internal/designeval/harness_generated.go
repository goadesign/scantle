// Package designeval reads compiler-owned source and schema output without
// interpreting transport expressions or rebuilding schemas.
package designeval

// harnessGeneratedHelpers is compiled into the sandboxed evaluator. Generated
// bytes remain in that process; only their digests and tool schemas are returned.
const harnessGeneratedHelpers = `
type (
	// toolInputBinding returns the distinct inputs a consumer can install.
	toolInputBinding struct {
		// ArgsSchema is ordinary application-value binding input.
		ArgsSchema string
		// TextOnlyArgsSchema is the ordinary UI-free input when supported.
		TextOnlyArgsSchema *string
		// LookupArgsSchema is the declared lookup-key input when available.
		LookupArgsSchema *string
		// LookupTextOnlyArgsSchema is the UI-free lookup alternative.
		LookupTextOnlyArgsSchema *string
		// InjectedFields names fields supplied outside ordinary input.
		InjectedFields []string
	}
	// catalogType is one named schema in the existing Goa-AI catalog.
	catalogType struct {
		// Schema is the compiler's advertised JSON Schema, not a reconstructed object.
		Schema json.RawMessage
	}
	// catalogBinding contains the ordinary and lookup inputs the application can install.
	catalogBinding struct {
		// Payload is the ordinary bound model input.
		Payload *catalogType
		// TextOnlyPayload is the ordinary UI-free input when supported.
		TextOnlyPayload *catalogType ` + "`json:\"text_only_payload\"`" + `
		// LookupPayload is the declared lookup-key input when available.
		LookupPayload *catalogType ` + "`json:\"lookup_payload\"`" + `
		// LookupTextOnlyPayload is the corresponding UI-free lookup input.
		LookupTextOnlyPayload *catalogType ` + "`json:\"lookup_text_only_payload\"`" + `
		// HiddenFields names fields supplied outside the ordinary model input.
		HiddenFields []string ` + "`json:\"hidden_fields\"`" + `
	}
	// catalogTool retains the compiler's exact identity, schemas, and metadata.
	catalogTool struct {
		// ID is the compiler's runtime tool identifier.
		ID string
		// Name is the authored name and is never inferred from ID or Toolset.
		Name string
		// Toolset is this agent's selected registration route.
		Toolset string
		// Description is the generated model-facing description.
		Description string
		// Payload is the provider's model-visible input before consumer binding.
		Payload *catalogType
		// Result is the model-visible result, absent for result-less tools.
		Result *catalogType
		// TextOnlyPayload is the provider's UI-free input when supported.
		TextOnlyPayload *catalogType ` + "`json:\"text_only_payload\"`" + `
		// InputBinding lists this exact consumer's available binding contracts.
		InputBinding *catalogBinding ` + "`json:\"input_binding\"`" + `
		// HiddenFields names runtime/provider-supplied inputs omitted from Payload.
		HiddenFields []string ` + "`json:\"hidden_fields\"`" + `
		// RequiresUI excludes the tool from text-only execution.
		RequiresUI bool ` + "`json:\"requires_ui\"`" + `
		// BoundedResult reports the out-of-band result bounds declaration.
		BoundedResult bool ` + "`json:\"bounded_result\"`" + `
		// Confirmation is present only when the generated spec configures approval.
		Confirmation json.RawMessage
	}
	// schemaCatalog is the generated catalog for one consuming agent.
	schemaCatalog struct {
		// AgentID supplies consumer identity without parsing generated directories.
		AgentID string ` + "`json:\"agent_id\"`" + `
		// Tools contains only the public tools selected by this consumer.
		Tools []catalogTool
	}
)

// recordGeneratedSources compares every generated source file, including HTTP,
// gRPC proto definitions, MCP dispatch, and plugin output. It reads tool cards
// from the existing compiler catalog rather than deriving their schemas.
func recordGeneratedSources(out *output, sources map[string][]byte) error {
	paths := make([]string, 0, len(sources))
	for path := range sources {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	seenTools := make(map[string]bool)
	for _, path := range paths {
		data := sources[path]
		out.Declarations["Generated:"+path] = fmt.Sprintf("%x", sha256.Sum256(data))
		if !strings.HasSuffix(path, "/specs/tool_schemas.json") {
			continue
		}
		var catalog schemaCatalog
		if err := json.Unmarshal(data, &catalog); err != nil {
			return fmt.Errorf("read generated catalog %s: %w", path, err)
		}
		if catalog.AgentID == "" {
			return fmt.Errorf("generated catalog %s has no agent_id; update the target's Goa-AI dependency", path)
		}
		for _, entry := range catalog.Tools {
			if entry.ID == "" || entry.Name == "" || entry.Toolset == "" || entry.Payload == nil {
				return fmt.Errorf("generated catalog %s has an incomplete tool record; update the target's Goa-AI dependency", path)
			}
			id := catalog.AgentID + "/" + entry.ID
			if seenTools[id] {
				return fmt.Errorf("generated catalogs repeat tool %s", id)
			}
			seenTools[id] = true
			card := toolContract{
				ID: id,
				Toolset: entry.Toolset,
				Name: entry.Name,
				Description: entry.Description,
				ArgsSchema: string(entry.Payload.Schema),
				InjectedFields: append([]string{}, entry.HiddenFields...),
				RequiresUI: entry.RequiresUI,
				RequiresConfirmation: len(entry.Confirmation) > 0 && string(entry.Confirmation) != "null",
				BoundedResult: entry.BoundedResult,
			}
			if entry.Result != nil {
				card.ReturnSchema = string(entry.Result.Schema)
			}
			if entry.TextOnlyPayload != nil {
				schema := string(entry.TextOnlyPayload.Schema)
				card.TextOnlyArgsSchema = &schema
			}
			if binding := entry.InputBinding; binding != nil {
				if binding.Payload == nil {
					return fmt.Errorf("generated tool %s has no ordinary bound schema", id)
				}
				card.InputBinding = &toolInputBinding{
					ArgsSchema: string(binding.Payload.Schema),
					InjectedFields: append([]string{}, binding.HiddenFields...),
				}
				if binding.TextOnlyPayload != nil {
					schema := string(binding.TextOnlyPayload.Schema)
					card.InputBinding.TextOnlyArgsSchema = &schema
				}
				if binding.LookupPayload != nil {
					schema := string(binding.LookupPayload.Schema)
					card.InputBinding.LookupArgsSchema = &schema
				}
				if binding.LookupTextOnlyPayload != nil {
					schema := string(binding.LookupTextOnlyPayload.Schema)
					card.InputBinding.LookupTextOnlyArgsSchema = &schema
				}
			}
			out.Tools = append(out.Tools, card)
		}
	}
	return nil
}
`

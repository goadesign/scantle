// Package designeval holds the standalone Goa/Goa-AI evaluator source template
// compiled and executed inside verify/candidate.Exec's OS-enforced sandbox.
package designeval

const (
	// harnessMainTemplate is the standalone evaluator program compiled and
	// executed inside candidate.Exec against the temporary design package.
	harnessMainTemplate = harnessMainHead + harnessMainHelpers

	// harnessMainHead contains the package declarations, structured record types,
	// and main evaluation flow of the standalone evaluator program.
	harnessMainHead = `package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"sort"

	_ {{DESIGN_IMPORT}}
	agentcodegen "goa.design/goa-ai/codegen/agent"
	"goa.design/goa-ai/codegen/jsonschema"
	evalexpr "goa.design/goa-ai/eval/expr"
	agentexpr "goa.design/goa-ai/expr/agent"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

type (
	c4Container struct {
		ID          string
		Name        string
		Kind        string
		Description string
	}

	c4Edge struct {
		FromID   string
		ToID     string
		Protocol string
	}

	agentPolicy struct {
		AgentID             string
		MaxExecutionSeconds int
		MaxToolCalls        int
		MaxRecoveryTurns    int
		CompressAtTurns     int
		KeepRecentTurns     int
		CacheAfterSystem    bool
		CacheAfterTools     bool
	}

	toolContract struct {
		ID                   string
		Toolset              string
		Name                 string
		Description          string
		ArgsSchema           string
		ReturnSchema         string
		InjectedFields       []string
		RequiresConfirmation bool
		BoundedResult        bool
	}

	output struct {
		SystemName    string            ` + "`json:\"system_name\"`" + `
		Declarations  map[string]string ` + "`json:\"declarations\"`" + `
		Containers    []c4Container     ` + "`json:\"containers\"`" + `
		Edges         []c4Edge          ` + "`json:\"edges\"`" + `
		AgentPolicies []agentPolicy     ` + "`json:\"agent_policies\"`" + `
		Tools         []toolContract    ` + "`json:\"tools\"`" + `
	}

	schemaDecl struct {
		Schema    json.RawMessage        ` + "`json:\"schema\"`" + `
		FieldMeta map[string]metaDeclMap ` + "`json:\"field_meta,omitempty\"`" + `
	}

	metaDeclMap map[string][]string

	mappedElemDecl struct {
		Elem   string ` + "`json:\"elem\"`" + `
		Target string ` + "`json:\"target\"`" + `
	}

	mappedAttDecl struct {
		Schema   schemaDecl       ` + "`json:\"schema\"`" + `
		Mappings []mappedElemDecl ` + "`json:\"mappings,omitempty\"`" + `
	}

	securityReqDecl struct {
		Schemes []string ` + "`json:\"schemes\"`" + `
		Scopes  []string ` + "`json:\"scopes,omitempty\"`" + `
	}

	errorDecl struct {
		Name        string     ` + "`json:\"name\"`" + `
		Description string     ` + "`json:\"description,omitempty\"`" + `
		Schema      schemaDecl ` + "`json:\"schema\"`" + `
		Temporary   bool       ` + "`json:\"temporary,omitempty\"`" + `
		Timeout     bool       ` + "`json:\"timeout,omitempty\"`" + `
		Fault       bool       ` + "`json:\"fault,omitempty\"`" + `
	}

	apiDecl struct {
		Name        string         ` + "`json:\"name\"`" + `
		Title       string         ` + "`json:\"title,omitempty\"`" + `
		Version     string         ` + "`json:\"version,omitempty\"`" + `
		Description string         ` + "`json:\"description,omitempty\"`" + `
		HTTPPrefix  string         ` + "`json:\"http_prefix,omitempty\"`" + `
		HTTPParams  *mappedAttDecl ` + "`json:\"http_params,omitempty\"`" + `
		HTTPHeaders *mappedAttDecl ` + "`json:\"http_headers,omitempty\"`" + `
		HTTPCookies *mappedAttDecl ` + "`json:\"http_cookies,omitempty\"`" + `
	}

	typeDecl struct {
		Name        string     ` + "`json:\"name\"`" + `
		Description string     ` + "`json:\"description,omitempty\"`" + `
		Schema      schemaDecl ` + "`json:\"schema\"`" + `
	}

	serviceDecl struct {
		Name        string            ` + "`json:\"name\"`" + `
		Description string            ` + "`json:\"description,omitempty\"`" + `
		HTTPPaths   []string          ` + "`json:\"http_paths,omitempty\"`" + `
		HTTPParams  *mappedAttDecl    ` + "`json:\"http_params,omitempty\"`" + `
		HTTPHeaders *mappedAttDecl    ` + "`json:\"http_headers,omitempty\"`" + `
		HTTPCookies *mappedAttDecl    ` + "`json:\"http_cookies,omitempty\"`" + `
		Security    []securityReqDecl ` + "`json:\"security,omitempty\"`" + `
		Errors      []errorDecl       ` + "`json:\"errors,omitempty\"`" + `
	}

	httpResponseDecl struct {
		StatusCode  int            ` + "`json:\"status_code\"`" + `
		Description string         ` + "`json:\"description,omitempty\"`" + `
		ContentType string         ` + "`json:\"content_type,omitempty\"`" + `
		View        string         ` + "`json:\"view,omitempty\"`" + `
		Tag         [2]string      ` + "`json:\"tag,omitempty\"`" + `
		Headers     *mappedAttDecl ` + "`json:\"headers,omitempty\"`" + `
		Cookies     *mappedAttDecl ` + "`json:\"cookies,omitempty\"`" + `
		Body        *schemaDecl    ` + "`json:\"body,omitempty\"`" + `
	}

	httpErrorDecl struct {
		Name     string           ` + "`json:\"name\"`" + `
		Response httpResponseDecl ` + "`json:\"response\"`" + `
	}

	httpEndpointDecl struct {
		Routes                       []string           ` + "`json:\"routes,omitempty\"`" + `
		Params                       *mappedAttDecl     ` + "`json:\"params,omitempty\"`" + `
		Headers                      *mappedAttDecl     ` + "`json:\"headers,omitempty\"`" + `
		Cookies                      *mappedAttDecl     ` + "`json:\"cookies,omitempty\"`" + `
		Body                         *schemaDecl        ` + "`json:\"body,omitempty\"`" + `
		StreamingBody                *schemaDecl        ` + "`json:\"streaming_body,omitempty\"`" + `
		MapQueryParams               *string            ` + "`json:\"map_query_params,omitempty\"`" + `
		MultipartRequest             bool               ` + "`json:\"multipart_request,omitempty\"`" + `
		FormRequest                  bool               ` + "`json:\"form_request,omitempty\"`" + `
		SkipRequestBodyEncodeDecode  bool               ` + "`json:\"skip_request_body_encode_decode,omitempty\"`" + `
		SkipResponseBodyEncodeDecode bool               ` + "`json:\"skip_response_body_encode_decode,omitempty\"`" + `
		Responses                    []httpResponseDecl ` + "`json:\"responses,omitempty\"`" + `
		Errors                       []httpErrorDecl    ` + "`json:\"errors,omitempty\"`" + `
	}

	grpcResponseDecl struct {
		StatusCode int            ` + "`json:\"status_code\"`" + `
		Message    *schemaDecl    ` + "`json:\"message,omitempty\"`" + `
		Headers    *mappedAttDecl ` + "`json:\"headers,omitempty\"`" + `
		Trailers   *mappedAttDecl ` + "`json:\"trailers,omitempty\"`" + `
	}

	grpcErrorDecl struct {
		Name     string           ` + "`json:\"name\"`" + `
		Response grpcResponseDecl ` + "`json:\"response\"`" + `
	}

	grpcEndpointDecl struct {
		Request          *schemaDecl      ` + "`json:\"request,omitempty\"`" + `
		StreamingRequest *schemaDecl      ` + "`json:\"streaming_request,omitempty\"`" + `
		Metadata         *mappedAttDecl   ` + "`json:\"metadata,omitempty\"`" + `
		Response         grpcResponseDecl ` + "`json:\"response\"`" + `
		Errors           []grpcErrorDecl  ` + "`json:\"errors,omitempty\"`" + `
	}

	methodDecl struct {
		Name             string            ` + "`json:\"name\"`" + `
		Description      string            ` + "`json:\"description,omitempty\"`" + `
		Payload          schemaDecl        ` + "`json:\"payload\"`" + `
		StreamingPayload *schemaDecl       ` + "`json:\"streaming_payload,omitempty\"`" + `
		Result           schemaDecl        ` + "`json:\"result\"`" + `
		Security         []securityReqDecl ` + "`json:\"security,omitempty\"`" + `
		Errors           []errorDecl       ` + "`json:\"errors,omitempty\"`" + `
		HTTP             *httpEndpointDecl ` + "`json:\"http,omitempty\"`" + `
		GRPC             *grpcEndpointDecl ` + "`json:\"grpc,omitempty\"`" + `
	}

	mcpAnnotationsDecl struct {
		Title           *string ` + "`json:\"title,omitempty\"`" + `
		ReadOnlyHint    *bool   ` + "`json:\"read_only_hint,omitempty\"`" + `
		DestructiveHint *bool   ` + "`json:\"destructive_hint,omitempty\"`" + `
		IdempotentHint  *bool   ` + "`json:\"idempotent_hint,omitempty\"`" + `
		OpenWorldHint   *bool   ` + "`json:\"open_world_hint,omitempty\"`" + `
	}

	mcpServerDecl struct {
		Name         string            ` + "`json:\"name\"`" + `
		Version      string            ` + "`json:\"version\"`" + `
		Description  string            ` + "`json:\"description,omitempty\"`" + `
		DynamicTools bool              ` + "`json:\"dynamic_tools,omitempty\"`" + `
		Security     []securityReqDecl ` + "`json:\"security,omitempty\"`" + `
	}

	mcpToolDecl struct {
		Name          string              ` + "`json:\"name\"`" + `
		Method        string              ` + "`json:\"method\"`" + `
		Description   string              ` + "`json:\"description,omitempty\"`" + `
		ContentField  string              ` + "`json:\"content_field,omitempty\"`" + `
		MetadataField string              ` + "`json:\"metadata_field,omitempty\"`" + `
		UIResourceURI string              ` + "`json:\"ui_resource_uri,omitempty\"`" + `
		Visibility    uint8               ` + "`json:\"visibility\"`" + `
		Annotations   *mcpAnnotationsDecl ` + "`json:\"annotations,omitempty\"`" + `
	}

	mcpResourceDecl struct {
		Name        string ` + "`json:\"name\"`" + `
		Description string ` + "`json:\"description,omitempty\"`" + `
		URI         string ` + "`json:\"uri\"`" + `
		MimeType    string ` + "`json:\"mime_type,omitempty\"`" + `
		Method      string ` + "`json:\"method,omitempty\"`" + `
	}

	mcpPromptDecl struct {
		Name        string ` + "`json:\"name\"`" + `
		Description string ` + "`json:\"description,omitempty\"`" + `
		Method      string ` + "`json:\"method,omitempty\"`" + `
	}

	inputBindingDecl struct {
		Tool   string   ` + "`json:\"tool\"`" + `
		Fields []string ` + "`json:\"fields\"`" + `
	}

	toolsetDecl struct {
		Name          string             ` + "`json:\"name\"`" + `
		Description   string             ` + "`json:\"description,omitempty\"`" + `
		Deferred      bool               ` + "`json:\"deferred,omitempty\"`" + `
		DeferredTools []string           ` + "`json:\"deferred_tools,omitempty\"`" + `
		InputBindings []inputBindingDecl ` + "`json:\"input_bindings,omitempty\"`" + `
		FieldBindings []string           ` + "`json:\"field_bindings,omitempty\"`" + `
	}

	toolConfirmDecl struct {
		Title                string ` + "`json:\"title,omitempty\"`" + `
		PromptTemplate       string ` + "`json:\"prompt_template,omitempty\"`" + `
		DeniedResultTemplate string ` + "`json:\"denied_result_template,omitempty\"`" + `
	}

	toolBoundsDecl struct {
		Bounded         bool   ` + "`json:\"bounded\"`" + `
		CursorField     string ` + "`json:\"cursor_field,omitempty\"`" + `
		NextCursorField string ` + "`json:\"next_cursor_field,omitempty\"`" + `
		ContinueTool    string ` + "`json:\"continue_tool,omitempty\"`" + `
	}

	toolDecl struct {
		Name            string           ` + "`json:\"name\"`" + `
		Description     string           ` + "`json:\"description,omitempty\"`" + `
		ArgsSchema      json.RawMessage  ` + "`json:\"args_schema\"`" + `
		ReturnSchema    json.RawMessage  ` + "`json:\"return_schema\"`" + `
		HiddenFields    []string         ` + "`json:\"hidden_fields,omitempty\"`" + `
		RequiresUI      bool             ` + "`json:\"requires_ui,omitempty\"`" + `
		UIOnlyFields    []string         ` + "`json:\"ui_only_fields,omitempty\"`" + `
		Confirmation    *toolConfirmDecl ` + "`json:\"confirmation,omitempty\"`" + `
		Bounds          *toolBoundsDecl  ` + "`json:\"bounds,omitempty\"`" + `
		TerminalRun     bool             ` + "`json:\"terminal_run,omitempty\"`" + `
		Bookkeeping     bool             ` + "`json:\"bookkeeping,omitempty\"`" + `
		ReplanOnTimeout bool             ` + "`json:\"replan_on_timeout,omitempty\"`" + `
	}

	agentDecl struct {
		Name        string      ` + "`json:\"name\"`" + `
		Description string      ` + "`json:\"description,omitempty\"`" + `
		Policy      agentPolicy ` + "`json:\"policy\"`" + `
	}

	suiteDecl struct {
		Name        string ` + "`json:\"name\"`" + `
		Description string ` + "`json:\"description,omitempty\"`" + `
		Timeout     string ` + "`json:\"timeout,omitempty\"`" + `
	}

	scenarioDecl struct {
		Name        string     ` + "`json:\"name\"`" + `
		Description string     ` + "`json:\"description,omitempty\"`" + `
		Tags        []string   ` + "`json:\"tags,omitempty\"`" + `
		Input       schemaDecl ` + "`json:\"input\"`" + `
	}
)

func main() {
	outPath := flag.String("out", "", "Output JSON path")
	flag.Parse()

	if err := eval.RunDSL(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	if err := agentcodegen.Prepare("", []eval.Root{goaexpr.Root, agentexpr.Root}); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}

	out := output{
		Declarations:  make(map[string]string),
		Containers:    make([]c4Container, 0),
		Edges:         make([]c4Edge, 0),
		AgentPolicies: make([]agentPolicy, 0),
		Tools:         make([]toolContract, 0),
	}

	seenContainers := make(map[string]bool)
	addContainer := func(c c4Container) {
		if seenContainers[c.ID] {
			return
		}
		seenContainers[c.ID] = true
		out.Containers = append(out.Containers, c)
	}
	seenEdges := make(map[string]bool)
	addEdge := func(e c4Edge) {
		k := e.FromID + "->" + e.ToID + ":" + e.Protocol
		if seenEdges[k] {
			return
		}
		seenEdges[k] = true
		out.Edges = append(out.Edges, e)
	}
	seenToolCardIDs := make(map[string]bool)
	addToolCard := func(tc toolContract) {
		if seenToolCardIDs[tc.ID] {
			return
		}
		seenToolCardIDs[tc.ID] = true
		out.Tools = append(out.Tools, tc)
	}

	api := goaexpr.Root.API
	if api != nil && api.Name != "" && api.Name != "api" {
		out.SystemName = api.Name
		decl := apiDecl{
			Name:        api.Name,
			Title:       api.Title,
			Version:     api.Version,
			Description: api.Description,
		}
		if api.HTTP != nil {
			decl.HTTPPrefix = api.HTTP.Path
			decl.HTTPParams = describeMappedAtt(api, api.HTTP.Params, "api.http.params")
			decl.HTTPHeaders = describeMappedAtt(api, api.HTTP.Headers, "api.http.headers")
			decl.HTTPCookies = describeMappedAtt(api, api.HTTP.Cookies, "api.http.cookies")
		}
		out.Declarations["API:"+api.Name] = mustJSON(decl)
	}

	for _, ut := range goaexpr.Root.Types {
		if ut == nil {
			continue
		}
		att := ut.Attribute()
		var desc string
		if att != nil {
			desc = att.Description
		}
		out.Declarations["Type:"+ut.Name()] = mustJSON(typeDecl{
			Name:        ut.Name(),
			Description: desc,
			Schema:      describeSchema(api, att, "type."+ut.Name()),
		})
	}

	for _, svc := range goaexpr.Root.Services {
		if svc == nil {
			continue
		}
		if out.SystemName == "" {
			out.SystemName = svc.Name
		}
		svcID := "service:" + svc.Name
		desc := svc.Description
		if desc == "" {
			desc = "Goa service " + svc.Name
		}
		addContainer(c4Container{
			ID:          svcID,
			Name:        svc.Name,
			Kind:        "service",
			Description: desc,
		})

		var httpSvc *goaexpr.HTTPServiceExpr
		if api != nil && api.HTTP != nil {
			httpSvc = api.HTTP.Service(svc.Name)
		}
		var grpcSvc *goaexpr.GRPCServiceExpr
		if api != nil && api.GRPC != nil {
			grpcSvc = api.GRPC.Service(svc.Name)
		}
		svcDecl := serviceDecl{
			Name:        svc.Name,
			Description: svc.Description,
			Security:    describeSecurity(svc.Requirements),
			Errors:      describeErrors(api, svc.Errors),
		}
		if httpSvc != nil {
			svcDecl.HTTPPaths = slices.Clone(httpSvc.Paths)
			slices.Sort(svcDecl.HTTPPaths)
			svcDecl.HTTPParams = describeMappedAtt(api, httpSvc.Params, "svc.http.params."+svc.Name)
			svcDecl.HTTPHeaders = describeMappedAtt(api, httpSvc.Headers, "svc.http.headers."+svc.Name)
			svcDecl.HTTPCookies = describeMappedAtt(api, httpSvc.Cookies, "svc.http.cookies."+svc.Name)
		}
		out.Declarations["Service:"+svc.Name] = mustJSON(svcDecl)

		for _, m := range svc.Methods {
			if m == nil {
				continue
			}
			mDecl := methodDecl{
				Name:        m.Name,
				Description: m.Description,
				Payload:     describeSchema(api, m.Payload, "method.payload."+svc.Name+"."+m.Name),
				Result:      describeSchema(api, m.Result, "method.result."+svc.Name+"."+m.Name),
				Security:    describeSecurity(m.Requirements),
				Errors:      describeErrors(api, m.Errors),
			}
			if m.StreamingPayload != nil && m.StreamingPayload.Type != nil && m.StreamingPayload.Type != goaexpr.Empty {
				sp := describeSchema(api, m.StreamingPayload, "method.stream_payload."+svc.Name+"."+m.Name)
				mDecl.StreamingPayload = &sp
			}
			if httpSvc != nil {
				if ep := httpSvc.Endpoint(m.Name); ep != nil {
					mDecl.HTTP = describeHTTPEndpoint(api, svc.Name, m.Name, ep)
				}
			}
			if grpcSvc != nil {
				if gep := grpcSvc.Endpoint(m.Name); gep != nil {
					mDecl.GRPC = describeGRPCEndpoint(api, svc.Name, m.Name, gep)
				}
			}
			key := fmt.Sprintf("Service:%s/Method:%s", svc.Name, m.Name)
			out.Declarations[key] = mustJSON(mDecl)
		}

		if mcpexpr.Root != nil {
			if mcp := mcpexpr.Root.GetMCP(svc); mcp != nil {
				mcpKey := fmt.Sprintf("Service:%s/MCP:%s", svc.Name, mcp.Name)
				out.Declarations[mcpKey] = mustJSON(mcpServerDecl{
					Name:         mcp.Name,
					Version:      mcp.Version,
					Description:  mcp.Description,
					DynamicTools: mcp.DynamicTools,
					Security:     describeSecurity(mcp.Requirements),
				})
				for _, t := range mcp.Tools {
					if t != nil && t.Method != nil {
						out.Declarations[fmt.Sprintf("%s/Tool:%s", mcpKey, t.Name)] = mustJSON(mcpToolDecl{
							Name:          t.Name,
							Method:        t.Method.Name,
							Description:   t.Description,
							ContentField:  t.ContentField,
							MetadataField: t.MetadataField,
							UIResourceURI: t.UIResourceURI,
							Visibility:    uint8(t.Visibility),
							Annotations:   describeMCPAnnotations(t.Annotations),
						})
					}
				}
				for _, r := range mcp.Resources {
					if r != nil {
						var methodName string
						if r.Method != nil {
							methodName = r.Method.Name
						}
						out.Declarations[fmt.Sprintf("%s/Resource:%s", mcpKey, r.Name)] = mustJSON(mcpResourceDecl{
							Name:        r.Name,
							Description: r.Description,
							URI:         r.URI,
							MimeType:    r.MimeType,
							Method:      methodName,
						})
					}
				}
				for _, rt := range mcp.ResourceTemplates {
					if rt != nil {
						out.Declarations[fmt.Sprintf("%s/ResourceTemplate:%s", mcpKey, rt.Name)] = mustJSON(mcpResourceDecl{
							Name:        rt.Name,
							Description: rt.Description,
							URI:         rt.URI,
							MimeType:    rt.MimeType,
						})
					}
				}
				for _, p := range mcp.Prompts {
					if p != nil {
						out.Declarations[fmt.Sprintf("%s/Prompt:%s", mcpKey, p.Name)] = mustJSON(mcpPromptDecl{
							Name:        p.Name,
							Description: p.Description,
						})
					}
				}
				for _, mp := range mcp.MethodPrompts {
					if mp != nil {
						var methodName string
						if mp.Method != nil {
							methodName = mp.Method.Name
						}
						out.Declarations[fmt.Sprintf("%s/MethodPrompt:%s", mcpKey, mp.Name)] = mustJSON(mcpPromptDecl{
							Name:        mp.Name,
							Description: mp.Description,
							Method:      methodName,
						})
					}
				}
			}
		}
	}

	recordToolset := func(ts *agentexpr.ToolsetExpr, scopePrefix string) string {
		if ts == nil {
			return ""
		}
		origin := ts
		if ts.Origin != nil {
			origin = ts.Origin
		}
		tsID := "toolset:" + origin.Name
		desc := origin.Description
		if desc == "" {
			desc = "Goa-AI toolset " + origin.Name
		}
		addContainer(c4Container{
			ID:          tsID,
			Name:        origin.Name,
			Kind:        "toolset",
			Description: desc,
		})
		tsDeclKey := "Toolset:" + origin.Name
		if scopePrefix != "" {
			tsDeclKey = scopePrefix + "/Toolset:" + origin.Name
		}

		var inputBinds []inputBindingDecl
		for _, ib := range ts.InputBindings {
			if ib != nil {
				fields := slices.Clone(ib.Fields)
				slices.Sort(fields)
				inputBinds = append(inputBinds, inputBindingDecl{
					Tool:   ib.Tool,
					Fields: fields,
				})
			}
		}
		slices.SortFunc(inputBinds, func(a, b inputBindingDecl) int {
			if a.Tool != b.Tool {
				if a.Tool < b.Tool {
					return -1
				}
				return 1
			}
			return slices.Compare(a.Fields, b.Fields)
		})
		var fieldBinds []string
		for _, fb := range ts.FieldBindings {
			if fb != nil {
				fieldBinds = append(fieldBinds, fb.Field)
			}
		}
		slices.Sort(fieldBinds)
		deferredTools := slices.Clone(ts.DeferredTools)
		slices.Sort(deferredTools)

		out.Declarations[tsDeclKey] = mustJSON(toolsetDecl{
			Name:          origin.Name,
			Description:   origin.Description,
			Deferred:      ts.Deferred,
			DeferredTools: deferredTools,
			InputBindings: inputBinds,
			FieldBindings: fieldBinds,
		})

		for _, t := range origin.Tools {
			if t == nil {
				continue
			}
			hidden := collectHiddenFields(t, ts, origin.Tools)
			argsSig := buildModelArgsSchemaJSON(api, t.Args, hidden, "tool.args."+origin.Name+"."+t.Name)
			retAtt := effectiveToolReturn(t)
			retSig := buildToolReturnSchemaJSON(api, retAtt, t.Bounds, "tool.return."+origin.Name+"."+t.Name)

			var confirmDecl *toolConfirmDecl
			if t.Confirmation != nil {
				confirmDecl = &toolConfirmDecl{
					Title:                t.Confirmation.Title,
					PromptTemplate:       t.Confirmation.PromptTemplate,
					DeniedResultTemplate: t.Confirmation.DeniedResultTemplate,
				}
			}
			var boundsDecl *toolBoundsDecl
			if t.Bounds != nil {
				boundsDecl = &toolBoundsDecl{Bounded: true}
				if t.Bounds.Paging != nil {
					boundsDecl.CursorField = t.Bounds.Paging.CursorField
					boundsDecl.NextCursorField = t.Bounds.Paging.NextCursorField
					boundsDecl.ContinueTool = t.Bounds.Paging.ContinueTool
				}
			}
			uiOnly := slices.Clone(t.UIOnlyFields)
			slices.Sort(uiOnly)
			toolKey := fmt.Sprintf("%s/Tool:%s", tsDeclKey, t.Name)
			out.Declarations[toolKey] = mustJSON(toolDecl{
				Name:            t.Name,
				Description:     t.Description,
				ArgsSchema:      json.RawMessage(argsSig),
				ReturnSchema:    json.RawMessage(retSig),
				HiddenFields:    hidden,
				RequiresUI:      t.RequiresUI,
				UIOnlyFields:    uiOnly,
				Confirmation:    confirmDecl,
				Bounds:          boundsDecl,
				TerminalRun:     t.TerminalRun,
				Bookkeeping:     t.Bookkeeping,
				ReplanOnTimeout: t.ReplanOnTimeout,
			})

			baseToolID := origin.Name + "." + t.Name
			providerHidden := collectHiddenFields(t, origin, origin.Tools)
			providerArgsSig := buildModelArgsSchemaJSON(api, t.Args, providerHidden, "tool.args."+origin.Name+"."+t.Name)
			addToolCard(toolContract{
				ID:                   baseToolID,
				Toolset:              origin.Name,
				Name:                 t.Name,
				Description:          t.Description,
				ArgsSchema:           providerArgsSig,
				ReturnSchema:         retSig,
				InjectedFields:       providerHidden,
				RequiresConfirmation: confirmDecl != nil,
				BoundedResult:        boundsDecl != nil,
			})
			if scopePrefix != "" && !slices.Equal(hidden, providerHidden) {
				addToolCard(toolContract{
					ID:                   scopePrefix + "/" + baseToolID,
					Toolset:              scopePrefix + "/" + origin.Name,
					Name:                 t.Name,
					Description:          t.Description,
					ArgsSchema:           argsSig,
					ReturnSchema:         retSig,
					InjectedFields:       hidden,
					RequiresConfirmation: confirmDecl != nil,
					BoundedResult:        boundsDecl != nil,
				})
			}

			if t.Method != nil && t.Method.Service != nil {
				addEdge(c4Edge{
					FromID:   tsID,
					ToID:     "service:" + t.Method.Service.Name,
					Protocol: "Goa Method Binding",
				})
			}
		}
		return tsID
	}

	if agentexpr.Root != nil {
		for _, ts := range agentexpr.Root.Toolsets {
			recordToolset(ts, "")
		}
		for _, se := range agentexpr.Root.ServiceExports {
			if se == nil || se.Service == nil {
				continue
			}
			for _, ts := range se.Toolsets {
				tsID := recordToolset(ts, "Service:"+se.Service.Name)
				if tsID != "" {
					addEdge(c4Edge{
						FromID:   "service:" + se.Service.Name,
						ToID:     tsID,
						Protocol: "Goa-AI Service Export",
					})
				}
			}
		}
		for _, ag := range agentexpr.Root.Agents {
			if ag == nil || ag.Service == nil {
				continue
			}
			svcID := "service:" + ag.Service.Name
			agID := fmt.Sprintf("agent:%s.%s", ag.Service.Name, ag.Name)
			desc := ag.Description
			if desc == "" {
				desc = "Goa-AI agent " + ag.Name
			}
			addContainer(c4Container{
				ID:          agID,
				Name:        ag.Name,
				Kind:        "agent",
				Description: desc,
			})
			addEdge(c4Edge{
				FromID:   svcID,
				ToID:     agID,
				Protocol: "Goa-AI Agent Workflow",
			})

			pol := agentPolicy{AgentID: agID}
			if ag.RunPolicy != nil {
				pol.MaxExecutionSeconds = int(ag.RunPolicy.TimeBudget.Seconds())
				if ag.RunPolicy.DefaultCaps != nil {
					pol.MaxToolCalls = ag.RunPolicy.DefaultCaps.MaxToolCalls
					pol.MaxRecoveryTurns = ag.RunPolicy.DefaultCaps.MaxRecoveryTurns
				}
				if ag.RunPolicy.History != nil {
					pol.CompressAtTurns = ag.RunPolicy.History.CompressAtTurns
					pol.KeepRecentTurns = ag.RunPolicy.History.KeepMaxTurns
					if pol.KeepRecentTurns == 0 {
						pol.KeepRecentTurns = ag.RunPolicy.History.KeepRecent
					}
				}
				if ag.RunPolicy.Cache != nil {
					pol.CacheAfterSystem = ag.RunPolicy.Cache.AfterSystem
					pol.CacheAfterTools = ag.RunPolicy.Cache.AfterTools
				}
			}
			out.AgentPolicies = append(out.AgentPolicies, pol)

			agDeclKey := fmt.Sprintf("Service:%s/Agent:%s", ag.Service.Name, ag.Name)
			out.Declarations[agDeclKey] = mustJSON(agentDecl{
				Name:        ag.Name,
				Description: ag.Description,
				Policy:      pol,
			})

			if ag.Exported != nil {
				for _, ts := range ag.Exported.Toolsets {
					tsID := recordToolset(ts, agDeclKey)
					if tsID != "" {
						addEdge(c4Edge{
							FromID:   tsID,
							ToID:     agID,
							Protocol: "Goa-AI AgentToolset",
						})
					}
				}
			}
			if ag.Used != nil {
				for _, ts := range ag.Used.Toolsets {
					tsID := recordToolset(ts, agDeclKey)
					if tsID != "" {
						addEdge(c4Edge{
							FromID:   agID,
							ToID:     tsID,
							Protocol: "Goa-AI Toolset",
						})
						if ts.Origin != nil && ts.Origin.Agent != nil && ts.Origin.Agent.Service != nil {
							providerAgID := fmt.Sprintf("agent:%s.%s", ts.Origin.Agent.Service.Name, ts.Origin.Agent.Name)
							addEdge(c4Edge{
								FromID:   tsID,
								ToID:     providerAgID,
								Protocol: "Goa-AI AgentToolset",
							})
						}
					}
				}
			}
		}
	}

	if evalexpr.Root != nil {
		for _, suite := range evalexpr.Root.Suites {
			if suite == nil {
				continue
			}
			suiteKey := "Suite:" + suite.Name
			out.Declarations[suiteKey] = mustJSON(suiteDecl{
				Name:        suite.Name,
				Description: suite.Description,
				Timeout:     suite.Timeout.String(),
			})
			for _, sc := range suite.Scenarios {
				if sc == nil {
					continue
				}
				tags := slices.Clone(sc.Tags)
				slices.Sort(tags)
				out.Declarations[fmt.Sprintf("%s/Scenario:%s", suiteKey, sc.Name)] = mustJSON(scenarioDecl{
					Name:        sc.Name,
					Description: sc.Description,
					Tags:        tags,
					Input:       describeSchema(api, sc.Input, "scenario.input."+suite.Name+"."+sc.Name),
				})
			}
		}
	}

	rawOut, err := json.Marshal(out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	if *outPath != "" {
		if err := os.WriteFile(*outPath, rawOut, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		return
	}
	os.Stdout.Write(rawOut)
}
`
)

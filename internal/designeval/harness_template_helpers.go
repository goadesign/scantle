// Package designeval holds the standalone Goa/Goa-AI evaluator helper
// functions compiled and executed inside verify/candidate.Exec's sandbox.
package designeval

const (
	// harnessMainHelpers contains the AST, transport, MCP, and Goa-AI schema
	// projection helpers appended to harnessMainHead to form harnessMainTemplate.
	harnessMainHelpers = `
func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func schemaJSON(api *goaexpr.APIExpr, att *goaexpr.AttributeExpr, id string) string {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return "{\"type\":\"null\"}"
	}
	if api == nil {
		api = &goaexpr.APIExpr{Name: "api", RandomizerFactory: goaexpr.NewDeterministicRandomizerFactory()}
	} else if api.RandomizerFactory == nil {
		api.RandomizerFactory = goaexpr.NewDeterministicRandomizerFactory()
	}
	identity := goaexpr.UserTypeExampleIdentity(&goaexpr.UserTypeExpr{TypeName: id, AttributeExpr: att})
	raw, err := jsonschema.Build(api, att, identity)
	if err != nil {
		return fmt.Sprintf("{\"error\":%q}", err.Error())
	}
	return string(raw)
}

func describeSchema(api *goaexpr.APIExpr, att *goaexpr.AttributeExpr, id string) schemaDecl {
	meta := make(map[string]metaDeclMap)
	collectAttributeMeta(att, "$", make(map[string]bool), meta)
	if len(meta) == 0 {
		meta = nil
	}
	return schemaDecl{
		Schema:    json.RawMessage(schemaJSON(api, att, id)),
		FieldMeta: meta,
	}
}

func collectAttributeMeta(att *goaexpr.AttributeExpr, path string, visited map[string]bool, out map[string]metaDeclMap) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return
	}
	if len(att.Meta) > 0 {
		copied := make(metaDeclMap, len(att.Meta))
		for k, vals := range att.Meta {
			copied[k] = slices.Clone(vals)
		}
		out[path] = copied
	}
	if ut, ok := att.Type.(goaexpr.UserType); ok && ut != nil {
		if visited[ut.ID()] {
			return
		}
		visited[ut.ID()] = true
		collectAttributeMeta(ut.Attribute(), path, visited, out)
		delete(visited, ut.ID())
		return
	}
	if obj := goaexpr.AsObject(att.Type); obj != nil {
		for _, nat := range *obj {
			if nat != nil {
				collectAttributeMeta(nat.Attribute, path+"."+nat.Name, visited, out)
			}
		}
		return
	}
	if arr := goaexpr.AsArray(att.Type); arr != nil {
		collectAttributeMeta(arr.ElemType, path+"[]", visited, out)
		return
	}
	if mp := goaexpr.AsMap(att.Type); mp != nil {
		collectAttributeMeta(mp.KeyType, path+".<key>", visited, out)
		collectAttributeMeta(mp.ElemType, path+".<elem>", visited, out)
		return
	}
	if un := goaexpr.AsUnion(att.Type); un != nil {
		for _, nat := range un.Values {
			if nat != nil {
				collectAttributeMeta(nat.Attribute, path+"|"+nat.Name, visited, out)
			}
		}
	}
}

func describeMappedAtt(api *goaexpr.APIExpr, m *goaexpr.MappedAttributeExpr, id string) *mappedAttDecl {
	if m == nil || m.AttributeExpr == nil || m.AttributeExpr.Type == nil || m.AttributeExpr.Type == goaexpr.Empty {
		return nil
	}
	obj := goaexpr.AsObject(m.AttributeExpr.Type)
	if obj != nil && len(*obj) == 0 {
		return nil
	}
	var mappings []mappedElemDecl
	if obj != nil {
		for _, nat := range *obj {
			if nat != nil {
				mappings = append(mappings, mappedElemDecl{
					Elem:   nat.Name,
					Target: m.ElemName(nat.Name),
				})
			}
		}
		slices.SortFunc(mappings, func(a, b mappedElemDecl) int {
			if a.Elem != b.Elem {
				if a.Elem < b.Elem {
					return -1
				}
				return 1
			}
			if a.Target < b.Target {
				return -1
			}
			if a.Target > b.Target {
				return 1
			}
			return 0
		})
	}
	return &mappedAttDecl{
		Schema:   describeSchema(api, m.AttributeExpr, id),
		Mappings: mappings,
	}
}

func describeHTTPResponse(api *goaexpr.APIExpr, prefix string, resp *goaexpr.HTTPResponseExpr) httpResponseDecl {
	if resp == nil {
		return httpResponseDecl{}
	}
	var view string
	if views, ok := resp.Meta["view"]; ok && len(views) > 0 {
		view = views[0]
	}
	d := httpResponseDecl{
		StatusCode:  resp.StatusCode,
		Description: resp.Description,
		ContentType: resp.ContentType,
		View:        view,
		Tag:         resp.Tag,
		Headers:     describeMappedAtt(api, resp.Headers, prefix+".headers"),
		Cookies:     describeMappedAtt(api, resp.Cookies, prefix+".cookies"),
	}
	if resp.Body != nil && resp.Body.Type != nil && resp.Body.Type != goaexpr.Empty {
		b := describeSchema(api, resp.Body, prefix+".body")
		d.Body = &b
	}
	return d
}

func describeHTTPEndpoint(api *goaexpr.APIExpr, svcName, methodName string, ep *goaexpr.HTTPEndpointExpr) *httpEndpointDecl {
	if ep == nil {
		return nil
	}
	prefix := fmt.Sprintf("http.%s.%s", svcName, methodName)
	var routes []string
	for _, r := range ep.Routes {
		if r != nil {
			for _, fp := range r.FullPaths() {
				routes = append(routes, r.Method+" "+fp)
			}
		}
	}
	slices.Sort(routes)
	d := &httpEndpointDecl{
		Routes:                       routes,
		Params:                       describeMappedAtt(api, ep.Params, prefix+".params"),
		Headers:                      describeMappedAtt(api, ep.Headers, prefix+".headers"),
		Cookies:                      describeMappedAtt(api, ep.Cookies, prefix+".cookies"),
		MapQueryParams:               ep.MapQueryParams,
		MultipartRequest:             ep.MultipartRequest,
		FormRequest:                  ep.FormRequest,
		SkipRequestBodyEncodeDecode:  ep.SkipRequestBodyEncodeDecode,
		SkipResponseBodyEncodeDecode: ep.SkipResponseBodyEncodeDecode,
	}
	if ep.Body != nil && ep.Body.Type != nil && ep.Body.Type != goaexpr.Empty {
		b := describeSchema(api, ep.Body, prefix+".body")
		d.Body = &b
	}
	if ep.StreamingBody != nil && ep.StreamingBody.Type != nil && ep.StreamingBody.Type != goaexpr.Empty {
		sb := describeSchema(api, ep.StreamingBody, prefix+".stream_body")
		d.StreamingBody = &sb
	}
	for i, resp := range ep.Responses {
		d.Responses = append(d.Responses, describeHTTPResponse(api, fmt.Sprintf("%s.resp.%d", prefix, i), resp))
	}
	for i, he := range ep.HTTPErrors {
		if he != nil && he.ErrorExpr != nil && he.Response != nil {
			d.Errors = append(d.Errors, httpErrorDecl{
				Name:     he.ErrorExpr.Name,
				Response: describeHTTPResponse(api, fmt.Sprintf("%s.err.%d.%s", prefix, i, he.ErrorExpr.Name), he.Response),
			})
		}
	}
	return d
}

func describeGRPCResponse(api *goaexpr.APIExpr, prefix string, resp *goaexpr.GRPCResponseExpr) grpcResponseDecl {
	if resp == nil {
		return grpcResponseDecl{}
	}
	d := grpcResponseDecl{
		StatusCode: resp.StatusCode,
		Headers:    describeMappedAtt(api, resp.Headers, prefix+".headers"),
		Trailers:   describeMappedAtt(api, resp.Trailers, prefix+".trailers"),
	}
	if resp.Message != nil && resp.Message.Type != nil && resp.Message.Type != goaexpr.Empty {
		msg := describeSchema(api, resp.Message, prefix+".msg")
		d.Message = &msg
	}
	return d
}

func describeGRPCEndpoint(api *goaexpr.APIExpr, svcName, methodName string, gep *goaexpr.GRPCEndpointExpr) *grpcEndpointDecl {
	if gep == nil {
		return nil
	}
	prefix := fmt.Sprintf("grpc.%s.%s", svcName, methodName)
	d := &grpcEndpointDecl{
		Metadata: describeMappedAtt(api, gep.Metadata, prefix+".metadata"),
		Response: describeGRPCResponse(api, prefix+".resp", gep.Response),
	}
	if gep.Request != nil && gep.Request.Type != nil && gep.Request.Type != goaexpr.Empty {
		req := describeSchema(api, gep.Request, prefix+".req")
		d.Request = &req
	}
	if gep.StreamingRequest != nil && gep.StreamingRequest.Type != nil && gep.StreamingRequest.Type != goaexpr.Empty {
		sreq := describeSchema(api, gep.StreamingRequest, prefix+".sreq")
		d.StreamingRequest = &sreq
	}
	for i, ge := range gep.GRPCErrors {
		if ge != nil && ge.ErrorExpr != nil && ge.Response != nil {
			d.Errors = append(d.Errors, grpcErrorDecl{
				Name:     ge.ErrorExpr.Name,
				Response: describeGRPCResponse(api, fmt.Sprintf("%s.err.%d.%s", prefix, i, ge.ErrorExpr.Name), ge.Response),
			})
		}
	}
	return d
}

func describeMCPAnnotations(a *mcpexpr.ToolAnnotationsExpr) *mcpAnnotationsDecl {
	if a == nil {
		return nil
	}
	return &mcpAnnotationsDecl{
		Title:           a.Title,
		ReadOnlyHint:    a.ReadOnlyHint,
		DestructiveHint: a.DestructiveHint,
		IdempotentHint:  a.IdempotentHint,
		OpenWorldHint:   a.OpenWorldHint,
	}
}

func isDedicatedContinuationTool(t *agentexpr.ToolExpr, tools []*agentexpr.ToolExpr) bool {
	if t == nil {
		return false
	}
	for _, candidate := range tools {
		if candidate != nil && candidate != t &&
			candidate.Bounds != nil &&
			candidate.Bounds.Paging != nil &&
			candidate.Bounds.Paging.ContinueTool == t.Name {
			return true
		}
	}
	return false
}

func effectiveObject(att *goaexpr.AttributeExpr) *goaexpr.Object {
	if att == nil || att.Type == nil {
		return nil
	}
	return goaexpr.AsObject(att.Type)
}

func hasObjectField(att *goaexpr.AttributeExpr, field string) bool {
	obj := effectiveObject(att)
	return obj != nil && obj.Attribute(field) != nil
}

func collectHiddenFields(t *agentexpr.ToolExpr, consumer *agentexpr.ToolsetExpr, allTools []*agentexpr.ToolExpr) []string {
	seen := make(map[string]bool)
	for _, f := range t.InjectedFields {
		seen[f] = true
	}
	if t.Bounds != nil && t.Bounds.Paging != nil && t.Bounds.Paging.ContinueTool != "" && t.Bounds.Paging.CursorField != "" {
		if hasObjectField(t.Args, t.Bounds.Paging.CursorField) {
			seen[t.Bounds.Paging.CursorField] = true
		}
	}
	if consumer != nil {
		for _, ib := range consumer.InputBindings {
			if ib != nil && ib.Tool == t.Name {
				for _, f := range ib.Fields {
					seen[f] = true
				}
			}
		}
		if !isDedicatedContinuationTool(t, allTools) {
			for _, fb := range consumer.FieldBindings {
				if fb != nil && hasObjectField(t.Args, fb.Field) {
					seen[fb.Field] = true
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	slices.Sort(out)
	return out
}

func unwrapUserTypeAttr(att *goaexpr.AttributeExpr) *goaexpr.AttributeExpr {
	if att == nil {
		return nil
	}
	cloned := goaexpr.DupAtt(att)
	for {
		ut, ok := cloned.Type.(goaexpr.UserType)
		if !ok || ut == nil || ut.Attribute() == nil {
			break
		}
		inner := goaexpr.DupAtt(ut.Attribute())
		if cloned.Description != "" {
			inner.Description = cloned.Description
		}
		cloned = inner
	}
	return cloned
}

func buildModelArgsSchemaJSON(api *goaexpr.APIExpr, att *goaexpr.AttributeExpr, hidden []string, id string) string {
	unwrapped := unwrapUserTypeAttr(att)
	if unwrapped != nil && len(hidden) > 0 {
		if obj := goaexpr.AsObject(unwrapped.Type); obj != nil {
			for _, field := range hidden {
				obj.Delete(field)
				if unwrapped.Validation != nil && len(unwrapped.Validation.Required) > 0 {
					unwrapped.Validation.Required = slices.DeleteFunc(unwrapped.Validation.Required, func(req string) bool {
						return req == field
					})
				}
			}
		}
	}
	raw := schemaJSON(api, unwrapped, id)
	if len(hidden) == 0 {
		return raw
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return raw
	}
	if props, ok := doc["properties"].(map[string]any); ok {
		for _, f := range hidden {
			delete(props, f)
		}
		if len(props) == 0 {
			delete(doc, "properties")
			delete(doc, "$defs")
		}
	}
	if reqs, ok := doc["required"].([]any); ok {
		kept := reqs[:0]
		for _, item := range reqs {
			name, _ := item.(string)
			if !slices.Contains(hidden, name) {
				kept = append(kept, item)
			}
		}
		if len(kept) == 0 {
			delete(doc, "required")
		} else {
			doc["required"] = kept
		}
	}
	if ex, ok := doc["example"].(map[string]any); ok {
		for _, f := range hidden {
			delete(ex, f)
		}
	}
	projected, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return string(projected)
}

func effectiveToolReturn(t *agentexpr.ToolExpr) *goaexpr.AttributeExpr {
	if t == nil {
		return nil
	}
	if (t.Return == nil || t.Return.Type == nil || t.Return.Type == goaexpr.Empty) &&
		t.Method != nil && t.Method.Result != nil && t.Method.Result.Type != goaexpr.Empty {
		return t.Method.Result
	}
	return t.Return
}

func buildToolReturnSchemaJSON(api *goaexpr.APIExpr, retAtt *goaexpr.AttributeExpr, bounds *agentexpr.ToolBoundsExpr, id string) string {
	raw := schemaJSON(api, unwrapUserTypeAttr(retAtt), id)
	if bounds == nil {
		return raw
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return raw
	}
	doc["type"] = "object"
	props, ok := doc["properties"].(map[string]any)
	if !ok || props == nil {
		props = make(map[string]any)
		doc["properties"] = props
	}
	props["returned"] = map[string]any{
		"type":        "integer",
		"description": "Number of items returned in this response after applying tool limits.",
	}
	props["total"] = map[string]any{
		"type":        "integer",
		"description": "Total number of matching items before truncation.",
	}
	props["truncated"] = map[string]any{
		"type":        "boolean",
		"description": "True when this result is partial because tool limits or caps were applied.",
	}
	props["refinement_hint"] = map[string]any{
		"type":        "string",
		"description": "Short guidance on how to narrow the request when the result is truncated.",
	}
	var nextCursorField string
	if bounds.Paging != nil && bounds.Paging.ContinueTool == "" && bounds.Paging.NextCursorField != "" {
		nextCursorField = bounds.Paging.NextCursorField
		props[nextCursorField] = map[string]any{
			"type":        "string",
			"description": "Continuation reference for the next page.",
		}
	}
	requiredSet := map[string]bool{
		"returned":  true,
		"truncated": true,
	}
	if retAtt != nil && retAtt.IsRequired("total") {
		requiredSet["total"] = true
	}
	optionalBounds := map[string]bool{
		"refinement_hint": true,
	}
	if !requiredSet["total"] {
		optionalBounds["total"] = true
	}
	if nextCursorField != "" {
		optionalBounds[nextCursorField] = true
	}
	if existingReq, ok := doc["required"].([]any); ok {
		for _, item := range existingReq {
			if name, ok := item.(string); ok && name != "" && !optionalBounds[name] {
				requiredSet[name] = true
			}
		}
	}
	mergedReq := make([]string, 0, len(requiredSet))
	for name := range requiredSet {
		mergedReq = append(mergedReq, name)
	}
	sort.Strings(mergedReq)
	doc["required"] = mergedReq

	projected, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return string(projected)
}

func describeSecurity(reqs []*goaexpr.SecurityExpr) []securityReqDecl {
	if len(reqs) == 0 {
		return nil
	}
	out := make([]securityReqDecl, 0, len(reqs))
	for _, r := range reqs {
		if r == nil {
			continue
		}
		var schemes []string
		for _, s := range r.Schemes {
			if s != nil {
				schemes = append(schemes, s.SchemeName)
			}
		}
		scopes := slices.Clone(r.Scopes)
		slices.Sort(schemes)
		slices.Sort(scopes)
		out = append(out, securityReqDecl{
			Schemes: schemes,
			Scopes:  scopes,
		})
	}
	return out
}

func describeErrors(api *goaexpr.APIExpr, errs []*goaexpr.ErrorExpr) []errorDecl {
	if len(errs) == 0 {
		return nil
	}
	out := make([]errorDecl, 0, len(errs))
	for _, e := range errs {
		if e != nil {
			_, temp := e.AttributeExpr.Meta["goa:error:temporary"]
			_, timeout := e.AttributeExpr.Meta["goa:error:timeout"]
			_, fault := e.AttributeExpr.Meta["goa:error:fault"]
			out = append(out, errorDecl{
				Name:        e.Name,
				Description: e.Description,
				Schema:      describeSchema(api, e.AttributeExpr, "err."+e.Name),
				Temporary:   temp,
				Timeout:     timeout,
				Fault:       fault,
			})
		}
	}
	slices.SortFunc(out, func(a, b errorDecl) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return out
}
`
)

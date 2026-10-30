// Package designeval holds the standalone Goa/Goa-AI/Model evaluator helper
// functions compiled and executed inside verify/candidate.Exec's sandbox.
package designeval

const (
	// harnessMainHelpers contains the AST, transport, MCP, Goa-AI, and Model schema
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
	goaTypes := make(map[string]string)
	protoTypes := make(map[string]string)
	meta := make(map[string]metaDeclMap)
	collectAttributeDetails(att, "$", make(map[string]bool), goaTypes, protoTypes, meta)
	if len(goaTypes) == 0 {
		goaTypes = nil
	}
	if len(protoTypes) == 0 {
		protoTypes = nil
	}
	if len(meta) == 0 {
		meta = nil
	}
	return schemaDecl{
		Schema:     json.RawMessage(schemaJSON(api, att, id)),
		GoaTypes:   goaTypes,
		ProtoTypes: protoTypes,
		FieldMeta:  meta,
	}
}

func describeGoaType(dt goaexpr.DataType) string {
	if dt == nil || dt == goaexpr.Empty {
		return "empty"
	}
	if ut, ok := dt.(goaexpr.UserType); ok && ut != nil {
		inner := "empty"
		if ut.Attribute() != nil {
			inner = describeGoaType(ut.Attribute().Type)
		}
		return "user:" + ut.Name() + "(" + inner + ")"
	}
	if obj := goaexpr.AsObject(dt); obj != nil {
		return "object"
	}
	if arr := goaexpr.AsArray(dt); arr != nil {
		var elem goaexpr.DataType
		if arr.ElemType != nil {
			elem = arr.ElemType.Type
		}
		return "array<" + describeGoaType(elem) + ">"
	}
	if mp := goaexpr.AsMap(dt); mp != nil {
		var key, elem goaexpr.DataType
		if mp.KeyType != nil {
			key = mp.KeyType.Type
		}
		if mp.ElemType != nil {
			elem = mp.ElemType.Type
		}
		return "map<" + describeGoaType(key) + "," + describeGoaType(elem) + ">"
	}
	if un := goaexpr.AsUnion(dt); un != nil {
		return "union:" + un.TypeName
	}
	return dt.Name()
}

func describeProtoType(att *goaexpr.AttributeExpr, dt goaexpr.DataType) string {
	if att != nil {
		if custom, ok := att.Meta["struct:field:proto"]; ok && len(custom) > 0 {
			return custom[0]
		}
	}
	if dt == nil || dt == goaexpr.Empty {
		return "empty"
	}
	switch dt.Kind() {
	case goaexpr.BooleanKind:
		return "bool"
	case goaexpr.IntKind, goaexpr.Int32Kind:
		return "sint32"
	case goaexpr.Int64Kind:
		return "sint64"
	case goaexpr.UIntKind, goaexpr.UInt32Kind:
		return "uint32"
	case goaexpr.UInt64Kind:
		return "uint64"
	case goaexpr.Float32Kind:
		return "float"
	case goaexpr.Float64Kind:
		return "double"
	case goaexpr.StringKind:
		return "string"
	case goaexpr.BytesKind:
		return "bytes"
	case goaexpr.AnyKind:
		return "google.protobuf.Value"
	case goaexpr.ArrayKind:
		arr := goaexpr.AsArray(dt)
		if arr == nil || arr.ElemType == nil {
			return "repeated unknown"
		}
		return "repeated " + describeProtoType(arr.ElemType, arr.ElemType.Type)
	case goaexpr.MapKind:
		mp := goaexpr.AsMap(dt)
		if mp == nil || mp.KeyType == nil || mp.ElemType == nil {
			return "map<unknown,unknown>"
		}
		return "map<" + describeProtoType(mp.KeyType, mp.KeyType.Type) + "," + describeProtoType(mp.ElemType, mp.ElemType.Type) + ">"
	case goaexpr.UnionKind:
		un := goaexpr.AsUnion(dt)
		if un == nil {
			return "oneof"
		}
		return "oneof:" + un.TypeName
	case goaexpr.UserTypeKind, goaexpr.ResultTypeKind:
		if ut, ok := dt.(goaexpr.UserType); ok && ut != nil {
			inner := "empty"
			if ut.Attribute() != nil {
				inner = describeProtoType(ut.Attribute(), ut.Attribute().Type)
			}
			return "message:" + ut.Name() + "(" + inner + ")"
		}
		return dt.Name()
	case goaexpr.ObjectKind:
		return "message"
	default:
		return dt.Name()
	}
}

func collectAttributeDetails(att *goaexpr.AttributeExpr, path string, visited map[string]bool, goaTypes, protoTypes map[string]string, out map[string]metaDeclMap) {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return
	}
	goaTypes[path] = describeGoaType(att.Type)
	pt := describeProtoType(att, att.Type)
	if tags, ok := att.Meta["rpc:tag"]; ok && len(tags) > 0 {
		pt += ":tag=" + tags[0]
	}
	protoTypes[path] = pt
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
		collectAttributeDetails(ut.Attribute(), path, visited, goaTypes, protoTypes, out)
		delete(visited, ut.ID())
		return
	}
	if obj := goaexpr.AsObject(att.Type); obj != nil {
		for _, nat := range *obj {
			if nat != nil {
				collectAttributeDetails(nat.Attribute, path+"."+nat.Name, visited, goaTypes, protoTypes, out)
			}
		}
		return
	}
	if arr := goaexpr.AsArray(att.Type); arr != nil {
		collectAttributeDetails(arr.ElemType, path+"[]", visited, goaTypes, protoTypes, out)
		return
	}
	if mp := goaexpr.AsMap(att.Type); mp != nil {
		collectAttributeDetails(mp.KeyType, path+".<key>", visited, goaTypes, protoTypes, out)
		collectAttributeDetails(mp.ElemType, path+".<elem>", visited, goaTypes, protoTypes, out)
		return
	}
	if un := goaexpr.AsUnion(att.Type); un != nil {
		for _, nat := range un.Values {
			if nat != nil {
				collectAttributeDetails(nat.Attribute, path+"|"+nat.Name, visited, goaTypes, protoTypes, out)
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
				return strings.Compare(a.Elem, b.Elem)
			}
			return strings.Compare(a.Target, b.Target)
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
		Security:                     describeSecurity(ep.Requirements),
	}
	if ep.Redirect != nil {
		d.Redirect = &httpRedirectDecl{
			URL:        ep.Redirect.URL,
			StatusCode: ep.Redirect.StatusCode,
		}
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
	if t == nil || t.Bounds == nil || t.Bounds.Paging == nil {
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

func collectHiddenAndLookupFields(t *agentexpr.ToolExpr, consumer *agentexpr.ToolsetExpr, allTools []*agentexpr.ToolExpr) ([]string, map[string]*goaexpr.AttributeExpr) {
	seen := make(map[string]bool)
	lookups := make(map[string]*goaexpr.AttributeExpr)
	if isDedicatedContinuationTool(t, allTools) {
		if obj := effectiveObject(t.Args); obj != nil {
			for _, nat := range *obj {
				if nat != nil {
					seen[nat.Name] = true
				}
			}
		}
	}
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
					delete(lookups, f)
				}
			}
		}
		if !isDedicatedContinuationTool(t, allTools) {
			for _, fb := range consumer.FieldBindings {
				if fb != nil && hasObjectField(t.Args, fb.Field) {
					if fb.Lookup == nil {
						seen[fb.Field] = true
						delete(lookups, fb.Field)
					} else if !seen[fb.Field] {
						lookups[fb.Field] = fb.Lookup
					}
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	slices.Sort(out)
	return out, lookups
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

func buildModelArgsSchemaJSON(api *goaexpr.APIExpr, att *goaexpr.AttributeExpr, hidden []string, lookups map[string]*goaexpr.AttributeExpr, id string) string {
	unwrapped := unwrapUserTypeAttr(att)
	if unwrapped != nil {
		if obj := goaexpr.AsObject(unwrapped.Type); obj != nil {
			for field, lk := range lookups {
				existing := obj.Attribute(field)
				if existing != nil && lk != nil {
					replacement := goaexpr.DupAtt(lk)
					if replacement.Description == "" && existing.Description != "" {
						replacement.Description = existing.Description
					}
					obj.Set(field, replacement)
				}
			}
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

func buildToolReturnSchemaJSON(api *goaexpr.APIExpr, retAtt, methodResult *goaexpr.AttributeExpr, bounds *agentexpr.ToolBoundsExpr, id string) string {
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
	if methodResult != nil && methodResult.IsRequired("total") {
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

func buildAgentPolicy(agID string, rp *agentexpr.RunPolicyExpr) agentPolicy {
	pol := agentPolicy{AgentID: agID}
	if rp == nil {
		return pol
	}
	pol.MaxExecutionSeconds = int(rp.TimeBudget.Seconds())
	if rp.TimeBudget > 0 {
		pol.TimeBudget = rp.TimeBudget.String()
	}
	if rp.PlanTimeout > 0 {
		pol.PlanTimeout = rp.PlanTimeout.String()
	}
	if rp.ToolTimeout > 0 {
		pol.ToolTimeout = rp.ToolTimeout.String()
	}
	pol.OnMissingFields = rp.OnMissingFields
	if rp.DefaultCaps != nil {
		pol.MaxToolCalls = rp.DefaultCaps.MaxToolCalls
		pol.MaxRecoveryTurns = rp.DefaultCaps.MaxRecoveryTurns
	}
	if rp.History != nil {
		pol.HistoryMode = string(rp.History.Mode)
		pol.CompressAtTurns = rp.History.CompressAtTurns
		pol.CompressAtMaxInputTokens = rp.History.CompressAtMaxInputTokens
		pol.KeepRecentTurns = rp.History.KeepRecent
		if pol.KeepRecentTurns == 0 {
			pol.KeepRecentTurns = rp.History.KeepMaxTurns
		}
		pol.KeepMaxInputTokens = rp.History.KeepMaxInputTokens
	}
	if rp.Cache != nil {
		pol.CacheAfterSystem = rp.Cache.AfterSystem
		pol.CacheAfterTools = rp.Cache.AfterTools
	}
	return pol
}

func describeSecurityScheme(s *goaexpr.SchemeExpr) securitySchemeDecl {
	if s == nil {
		return securitySchemeDecl{}
	}
	var scopes []securityScopeDecl
	for _, sc := range s.Scopes {
		if sc != nil {
			scopes = append(scopes, securityScopeDecl{
				Name:        sc.Name,
				Description: sc.Description,
			})
		}
	}
	slices.SortFunc(scopes, func(a, b securityScopeDecl) int {
		return strings.Compare(a.Name, b.Name)
	})
	var flows []securityFlowDecl
	for _, f := range s.Flows {
		if f != nil {
			flows = append(flows, securityFlowDecl{
				Kind:             f.Type(),
				AuthorizationURL: f.AuthorizationURL,
				TokenURL:         f.TokenURL,
				RefreshURL:       f.RefreshURL,
			})
		}
	}
	slices.SortFunc(flows, func(a, b securityFlowDecl) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		return strings.Compare(a.TokenURL, b.TokenURL)
	})
	return securitySchemeDecl{
		SchemeName:   s.SchemeName,
		Kind:         s.Kind.String(),
		Description:  s.Description,
		In:           s.In,
		Name:         s.Name,
		BearerFormat: s.BearerFormat,
		Scopes:       scopes,
		Flows:        flows,
	}
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
		var details []securitySchemeDecl
		for _, s := range r.Schemes {
			if s != nil {
				schemes = append(schemes, s.SchemeName)
				details = append(details, describeSecurityScheme(s))
			}
		}
		scopes := slices.Clone(r.Scopes)
		slices.Sort(schemes)
		slices.SortFunc(details, func(a, b securitySchemeDecl) int {
			return strings.Compare(a.SchemeName, b.SchemeName)
		})
		slices.Sort(scopes)
		out = append(out, securityReqDecl{
			Schemes:       schemes,
			SchemeDetails: details,
			Scopes:        scopes,
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
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

func describeModelElement(kind string, el *modelexpr.Element, addEdge func(c4Edge), fromID string) modelElementDecl {
	if el == nil {
		return modelElementDecl{Kind: kind}
	}
	var rels []modelRelDecl
	for _, r := range el.Relationships {
		if r == nil {
			continue
		}
		dest := r.DestinationPath
		if r.Destination != nil && r.Destination.Name != "" {
			dest = r.Destination.Name
		}
		rels = append(rels, modelRelDecl{
			Destination:     dest,
			Description:     r.Description,
			Technology:      r.Technology,
			InteractionKind: int(r.InteractionStyle),
			Tags:            r.Tags,
		})
		if fromID != "" && dest != "" {
			proto := r.Technology
			if proto == "" {
				proto = r.Description
			}
			if proto == "" {
				proto = "C4 Uses"
			}
			addEdge(c4Edge{
				FromID:   fromID,
				ToID:     "model:" + dest,
				Protocol: proto,
			})
		}
	}
	slices.SortFunc(rels, func(a, b modelRelDecl) int {
		if a.Destination != b.Destination {
			return strings.Compare(a.Destination, b.Destination)
		}
		return strings.Compare(a.Description, b.Description)
	})
	return modelElementDecl{
		Kind:          kind,
		Name:          el.Name,
		Description:   el.Description,
		Technology:    el.Technology,
		Tags:          el.Tags,
		URL:           el.URL,
		Properties:    el.Properties,
		Relationships: rels,
	}
}

func recordModelRoot(out *output, addContainer func(c4Container), addEdge func(c4Edge)) {
	if modelexpr.Root == nil || modelexpr.Root.Model == nil {
		return
	}
	m := modelexpr.Root.Model
	for _, p := range m.People {
		if p == nil || p.Element == nil {
			continue
		}
		id := "person:" + p.Name
		addContainer(c4Container{
			ID:          id,
			Name:        p.Name,
			Kind:        "person",
			Description: p.Description,
		})
		out.Declarations["Model:Person:"+p.Name] = mustJSON(describeModelElement("person", p.Element, addEdge, id))
	}
	for _, sys := range m.Systems {
		if sys == nil || sys.Element == nil {
			continue
		}
		if out.SystemName == "" {
			out.SystemName = sys.Name
		}
		sysID := "system:" + sys.Name
		addContainer(c4Container{
			ID:          sysID,
			Name:        sys.Name,
			Kind:        "system",
			Description: sys.Description,
		})
		sysKey := "Model:System:" + sys.Name
		out.Declarations[sysKey] = mustJSON(describeModelElement("system", sys.Element, addEdge, sysID))
		for _, c := range sys.Containers {
			if c == nil || c.Element == nil {
				continue
			}
			cID := fmt.Sprintf("container:%s.%s", sys.Name, c.Name)
			addContainer(c4Container{
				ID:          cID,
				Name:        c.Name,
				Kind:        "container",
				Description: c.Description,
			})
			cKey := fmt.Sprintf("%s/Container:%s", sysKey, c.Name)
			out.Declarations[cKey] = mustJSON(describeModelElement("container", c.Element, addEdge, cID))
			for _, cmp := range c.Components {
				if cmp == nil || cmp.Element == nil {
					continue
				}
				cmpKey := fmt.Sprintf("%s/Component:%s", cKey, cmp.Name)
				out.Declarations[cmpKey] = mustJSON(describeModelElement("component", cmp.Element, addEdge, ""))
			}
		}
	}
}
`
)

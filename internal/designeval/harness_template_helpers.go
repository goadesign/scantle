// Package designeval holds the standalone Goa/Goa-AI/Model evaluator helper
// functions compiled and executed inside verify/candidate.Exec's sandbox.
package designeval

const (
	// harnessMainHelpers describes domain declarations, runtime policy, and
	// resolved Model relationships. Registered generators own transport output.
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
		panic(err)
	}
	return string(raw)
}

func describeSchema(api *goaexpr.APIExpr, att *goaexpr.AttributeExpr, id string) schemaDecl {
	return schemaDecl{Schema: json.RawMessage(schemaJSON(api, att, id))}
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

// describeModelElement records Model's resolved relationship identities, which
// are shared by the graph nodes and distinguish equal names in different scopes.
func describeModelElement(kind string, el *modelexpr.Element, addEdge func(c4Edge)) modelElementDecl {
	if el == nil {
		return modelElementDecl{Kind: kind}
	}
	var rels []modelRelDecl
	for _, r := range el.Relationships {
		if r == nil {
			continue
		}
		dest := "model:" + r.Destination.ID
		rels = append(rels, modelRelDecl{
			Destination:     dest,
			Description:     r.Description,
			Technology:      r.Technology,
			InteractionKind: int(r.InteractionStyle),
			Tags:            r.Tags,
		})
		proto := r.Technology
		if proto == "" {
			proto = r.Description
		}
		if proto == "" {
			proto = "C4 Uses"
		}
		addEdge(c4Edge{
			FromID:   "model:" + el.ID,
			ToID:     dest,
			Protocol: proto,
		})
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

// recordModelRoot uses each evaluated Model ID for every node and relationship;
// Model owns parent scoping and has already resolved each destination.
func recordModelRoot(out *output, addContainer func(c4Container), addEdge func(c4Edge)) {
	if modelexpr.Root == nil || modelexpr.Root.Model == nil {
		return
	}
	m := modelexpr.Root.Model
	for _, p := range m.People {
		if p == nil || p.Element == nil {
			continue
		}
		id := "model:" + p.ID
		addContainer(c4Container{
			ID:          id,
			Name:        p.Name,
			Kind:        "person",
			Description: p.Description,
		})
		out.Declarations["Model:Person:"+p.Name] = mustJSON(describeModelElement("person", p.Element, addEdge))
	}
	for _, sys := range m.Systems {
		if sys == nil || sys.Element == nil {
			continue
		}
		if out.SystemName == "" {
			out.SystemName = sys.Name
		}
		sysID := "model:" + sys.ID
		addContainer(c4Container{
			ID:          sysID,
			Name:        sys.Name,
			Kind:        "system",
			Description: sys.Description,
		})
		sysKey := "Model:System:" + sys.Name
		out.Declarations[sysKey] = mustJSON(describeModelElement("system", sys.Element, addEdge))
		for _, c := range sys.Containers {
			if c == nil || c.Element == nil {
				continue
			}
			cID := "model:" + c.ID
			addContainer(c4Container{
				ID:          cID,
				Name:        c.Name,
				Kind:        "container",
				Description: c.Description,
			})
			cKey := fmt.Sprintf("%s/Container:%s", sysKey, c.Name)
			out.Declarations[cKey] = mustJSON(describeModelElement("container", c.Element, addEdge))
			for _, cmp := range c.Components {
				if cmp == nil || cmp.Element == nil {
					continue
				}
				cmpKey := fmt.Sprintf("%s/Component:%s", cKey, cmp.Name)
				addContainer(c4Container{
					ID:          "model:" + cmp.ID,
					Name:        cmp.Name,
					Kind:        "component",
					Description: cmp.Description,
				})
				out.Declarations[cmpKey] = mustJSON(describeModelElement("component", cmp.Element, addEdge))
			}
		}
	}
}
`
)

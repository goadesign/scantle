// Package designeval evaluates Go/Goa/Goa-AI design sources using the real Goa
// and Goa-AI DSL engine (`goa.design/goa/v3/eval.RunDSL`) so structural diffs
// and Checkpoint 2 architecture previews derive all facts from compiled,
// validated DSL expressions rather than a second AST interpreter.
package designeval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	genscantle "github.com/goadesign/scantle/gen/scantle"
)

type (
	// Result holds the canonical declarations, C4 topology, agent policies, and
	// tool contracts extracted from an evaluated Goa/Goa-AI design.
	Result struct {
		// SystemName is the evaluated API name or primary service name.
		SystemName string `json:"system_name"`
		// Declarations maps each scoped declaration key to its canonical evaluated contract signature.
		Declarations map[string]string `json:"declarations"`
		// Containers lists the C4 containers derived from evaluated Services, Agents, and Toolsets.
		Containers []*genscantle.C4ContainerItem `json:"containers"`
		// Edges lists the validated directed contract edges between C4 containers.
		Edges []*genscantle.C4EdgeItem `json:"edges"`
		// AgentPolicies lists the evaluated Goa-AI RunPolicy settings per agent.
		AgentPolicies []*genscantle.AgentPolicyItem `json:"agent_policies"`
		// Tools lists the evaluated Goa-AI tool contracts across all declared toolsets.
		Tools []*genscantle.ToolContractItem `json:"tools"`
	}
)

const (
	// harnessMainSource is the standalone evaluator program executed via `go run`
	// against the temporary design package.
	harnessMainSource = `package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	_ "github.com/goadesign/scantle/design"
	evalexpr "goa.design/goa-ai/eval/expr"
	agentexpr "goa.design/goa-ai/expr/agent"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

type c4Container struct {
	ID          string
	Name        string
	Kind        string
	Description string
}

type c4Edge struct {
	FromID   string
	ToID     string
	Protocol string
}

type agentPolicy struct {
	AgentID             string
	MaxExecutionSeconds int
	MaxToolCalls        int
	MaxRecoveryTurns    int
	CompressAtTurns     int
	KeepRecentTurns     int
	CacheAfterSystem    bool
	CacheAfterTools     bool
}

type toolContract struct {
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

type output struct {
	SystemName    string            ` + "`json:\"system_name\"`" + `
	Declarations  map[string]string ` + "`json:\"declarations\"`" + `
	Containers    []c4Container     ` + "`json:\"containers\"`" + `
	Edges         []c4Edge          ` + "`json:\"edges\"`" + `
	AgentPolicies []agentPolicy     ` + "`json:\"agent_policies\"`" + `
	Tools         []toolContract    ` + "`json:\"tools\"`" + `
}

func main() {
	if err := eval.RunDSL(); err != nil {
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
	seenTools := make(map[string]bool)

	if goaexpr.Root.API != nil && goaexpr.Root.API.Name != "" && goaexpr.Root.API.Name != "api" {
		out.SystemName = goaexpr.Root.API.Name
		out.Declarations["API:"+goaexpr.Root.API.Name] = fmt.Sprintf("title=%s;version=%s;desc=%s", goaexpr.Root.API.Title, goaexpr.Root.API.Version, goaexpr.Root.API.Description)
	}

	for _, ut := range goaexpr.Root.Types {
		if ut == nil {
			continue
		}
		out.Declarations["Type:"+ut.Name()] = describeAttr(ut.Attribute(), make(map[string]bool))
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
		out.Declarations["Service:"+svc.Name] = fmt.Sprintf("desc=%s", svc.Description)

		var httpSvc *goaexpr.HTTPServiceExpr
		if goaexpr.Root.API != nil && goaexpr.Root.API.HTTP != nil {
			httpSvc = goaexpr.Root.API.HTTP.Service(svc.Name)
		}
		for _, m := range svc.Methods {
			if m == nil {
				continue
			}
			var routes []string
			if httpSvc != nil {
				if ep := httpSvc.Endpoint(m.Name); ep != nil {
					for _, r := range ep.Routes {
						routes = append(routes, r.Method+" "+r.Path)
					}
				}
			}
			slices.Sort(routes)
			key := fmt.Sprintf("Service:%s/Method:%s", svc.Name, m.Name)
			out.Declarations[key] = fmt.Sprintf(
				"desc=%s;payload=%s;result=%s;routes=%s",
				m.Description,
				describeAttr(m.Payload, make(map[string]bool)),
				describeAttr(m.Result, make(map[string]bool)),
				strings.Join(routes, ","),
			)
		}

		if mcpexpr.Root != nil {
			if mcp := mcpexpr.Root.GetMCP(svc); mcp != nil {
				mcpKey := fmt.Sprintf("Service:%s/MCP:%s", svc.Name, mcp.Name)
				out.Declarations[mcpKey] = fmt.Sprintf("version=%s", mcp.Version)
				for _, t := range mcp.Tools {
					if t != nil && t.Method != nil {
						out.Declarations[fmt.Sprintf("%s/Tool:%s", mcpKey, t.Name)] = fmt.Sprintf("method=%s;desc=%s", t.Method.Name, t.Description)
					}
				}
				for _, r := range mcp.Resources {
					if r != nil {
						out.Declarations[fmt.Sprintf("%s/Resource:%s", mcpKey, r.Name)] = fmt.Sprintf("uri=%s;mime=%s", r.URI, r.MimeType)
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
		out.Declarations[tsDeclKey] = fmt.Sprintf("desc=%s", origin.Description)

		for _, t := range origin.Tools {
			if t == nil {
				continue
			}
			argsSig := describeAttr(t.Args, make(map[string]bool))
			retSig := describeAttr(t.Return, make(map[string]bool))
			inj := slices.Clone(t.InjectedFields)
			slices.Sort(inj)
			if inj == nil {
				inj = []string{}
			}
			hasConfirm := t.Confirmation != nil
			hasBounds := t.Bounds != nil
			toolKey := fmt.Sprintf("%s/Tool:%s", tsDeclKey, t.Name)
			out.Declarations[toolKey] = fmt.Sprintf(
				"desc=%s;args=%s;return=%s;inject=%s;confirm=%t;bounds=%t",
				t.Description,
				argsSig,
				retSig,
				strings.Join(inj, ","),
				hasConfirm,
				hasBounds,
			)
			fullToolID := origin.Name + "." + t.Name
			if !seenTools[fullToolID] {
				seenTools[fullToolID] = true
				out.Tools = append(out.Tools, toolContract{
					ID:                   fullToolID,
					Toolset:              origin.Name,
					Name:                 t.Name,
					Description:          t.Description,
					ArgsSchema:           argsSig,
					ReturnSchema:         retSig,
					InjectedFields:       inj,
					RequiresConfirmation: hasConfirm,
					BoundedResult:        hasBounds,
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
			out.Declarations[agDeclKey] = fmt.Sprintf(
				"desc=%s;budget=%ds;tools=%d;recovery=%d;compress=%d;keep=%d;cacheSys=%t;cacheTools=%t",
				ag.Description,
				pol.MaxExecutionSeconds,
				pol.MaxToolCalls,
				pol.MaxRecoveryTurns,
				pol.CompressAtTurns,
				pol.KeepRecentTurns,
				pol.CacheAfterSystem,
				pol.CacheAfterTools,
			)

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
			out.Declarations[suiteKey] = fmt.Sprintf("desc=%s;timeout=%s", suite.Description, suite.Timeout)
			for _, sc := range suite.Scenarios {
				if sc == nil {
					continue
				}
				out.Declarations[fmt.Sprintf("%s/Scenario:%s", suiteKey, sc.Name)] = fmt.Sprintf(
					"desc=%s;tags=%s;input=%s",
					sc.Description,
					strings.Join(sc.Tags, ","),
					describeAttr(sc.Input, make(map[string]bool)),
				)
			}
		}
	}

	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func describeAttr(att *goaexpr.AttributeExpr, visiting map[string]bool) string {
	if att == nil || att.Type == nil || att.Type == goaexpr.Empty {
		return "empty"
	}
	var b strings.Builder
	describeDataType(&b, att.Type, visiting)
	if v := att.Validation; v != nil {
		if v.Minimum != nil {
			fmt.Fprintf(&b, "[min=%v]", *v.Minimum)
		}
		if v.Maximum != nil {
			fmt.Fprintf(&b, "[max=%v]", *v.Maximum)
		}
		if v.MinLength != nil {
			fmt.Fprintf(&b, "[minLen=%d]", *v.MinLength)
		}
		if v.MaxLength != nil {
			fmt.Fprintf(&b, "[maxLen=%d]", *v.MaxLength)
		}
		if v.Pattern != "" {
			fmt.Fprintf(&b, "[pattern=%s]", v.Pattern)
		}
		if len(v.Values) > 0 {
			vals := make([]string, 0, len(v.Values))
			for _, val := range v.Values {
				vals = append(vals, fmt.Sprintf("%v", val))
			}
			slices.Sort(vals)
			fmt.Fprintf(&b, "[enum=%s]", strings.Join(vals, "|"))
		}
		if len(v.Required) > 0 {
			req := slices.Clone(v.Required)
			slices.Sort(req)
			fmt.Fprintf(&b, "[req=%s]", strings.Join(req, ","))
		}
	}
	return b.String()
}

func describeDataType(b *strings.Builder, dt goaexpr.DataType, visiting map[string]bool) {
	switch t := dt.(type) {
	case goaexpr.Primitive:
		b.WriteString(t.Name())
	case *goaexpr.Array:
		b.WriteString("[]")
		b.WriteString(describeAttr(t.ElemType, visiting))
	case *goaexpr.Map:
		b.WriteString("map[")
		b.WriteString(describeAttr(t.KeyType, visiting))
		b.WriteString("]")
		b.WriteString(describeAttr(t.ElemType, visiting))
	case *goaexpr.Object:
		b.WriteString("{")
		names := make([]string, 0, len(*t))
		byName := make(map[string]*goaexpr.AttributeExpr, len(*t))
		for _, nat := range *t {
			names = append(names, nat.Name)
			byName[nat.Name] = nat.Attribute
		}
		slices.Sort(names)
		for i, name := range names {
			if i > 0 {
				b.WriteString(";")
			}
			b.WriteString(name)
			b.WriteString(":")
			b.WriteString(describeAttr(byName[name], visiting))
		}
		b.WriteString("}")
	case goaexpr.UserType:
		b.WriteString(t.Name())
		if visiting[t.ID()] {
			return
		}
		visiting[t.ID()] = true
		b.WriteString("(")
		b.WriteString(describeAttr(t.Attribute(), visiting))
		b.WriteString(")")
		delete(visiting, t.ID())
	default:
		b.WriteString(dt.Name())
	}
}
`
)

// Evaluate compiles and executes a Goa/Goa-AI design source string using the
// real Goa evaluation engine (`eval.RunDSL`) and returns the evaluated Result.
func Evaluate(ctx context.Context, designSource string) (Result, error) {
	if strings.TrimSpace(designSource) == "" {
		return Result{
			Declarations:  make(map[string]string),
			Containers:    []*genscantle.C4ContainerItem{},
			Edges:         []*genscantle.C4EdgeItem{},
			AgentPolicies: []*genscantle.AgentPolicyItem{},
			Tools:         []*genscantle.ToolContractItem{},
		}, nil
	}

	modRoot, err := locateModuleRoot()
	if err != nil {
		return Result{}, err
	}

	tmpDir, err := os.MkdirTemp("", "scantle-designeval-*")
	if err != nil {
		return Result{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := os.Symlink(filepath.Join(modRoot, "go.mod"), filepath.Join(tmpDir, "go.mod")); err != nil {
		return Result{}, fmt.Errorf("symlink go.mod: %w", err)
	}
	if err := os.Symlink(filepath.Join(modRoot, "go.sum"), filepath.Join(tmpDir, "go.sum")); err != nil {
		return Result{}, fmt.Errorf("symlink go.sum: %w", err)
	}

	designDir := filepath.Join(tmpDir, "design")
	if err := os.MkdirAll(designDir, 0o750); err != nil {
		return Result{}, fmt.Errorf("mkdir design: %w", err)
	}
	if err := os.WriteFile(filepath.Join(designDir, "design.go"), []byte(designSource), 0o600); err != nil {
		return Result{}, fmt.Errorf("write design.go: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(harnessMainSource), 0o600); err != nil {
		return Result{}, fmt.Errorf("write evaluator main.go: %w", err)
	}

	cmd := exec.CommandContext(ctx, "go", "run", ".")
	cmd.Dir = tmpDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("evaluate Goa design: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}

	var res Result
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return Result{}, fmt.Errorf("decode evaluated Goa design: %w", err)
	}
	return res, nil
}

// locateModuleRoot walks up from this source file's directory to find the
// enclosing repository's go.mod file.
func locateModuleRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("unable to locate caller path for module root")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found above internal/designeval")
		}
		dir = parent
	}
}

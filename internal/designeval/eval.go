// Package designeval evaluates Go/Goa/Goa-AI design sources by compiling a
// standalone evaluator binary and executing it inside verify/candidate.Exec's
// OS-enforced single-process sandbox, using Goa-AI's canonical preparation and
// JSON Schema builders (`codegen/agent.Prepare` and `codegen/jsonschema.Build`).
package designeval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	genscantle "github.com/goadesign/scantle/gen/scantle"
	"github.com/goadesign/scantle/verify/candidate"
	"golang.org/x/mod/modfile"
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

	// toolchainEnv holds the resolved Go toolchain root, module cache directory,
	// and private sandboxed build work directory for candidate.Build.
	toolchainEnv struct {
		// goroot is the resolved Go toolchain root.
		goroot string
		// modCache is the read-only Go module cache directory.
		modCache string
		// workDir is the private writable directory for GOCACHE, HOME, and TMPDIR.
		workDir string
	}
)

const (
	// harnessMainTemplate is the standalone evaluator program compiled and
	// executed inside candidate.Exec against the temporary design package.
	harnessMainTemplate = `package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	_ {{DESIGN_IMPORT}}
	agentcodegen "goa.design/goa-ai/codegen/agent"
	"goa.design/goa-ai/codegen/jsonschema"
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
	toolIndexByID := make(map[string]int)

	api := goaexpr.Root.API
	if api != nil && api.Name != "" && api.Name != "api" {
		out.SystemName = api.Name
		var httpPrefix string
		if api.HTTP != nil {
			httpPrefix = api.HTTP.Path
		}
		out.Declarations["API:"+api.Name] = fmt.Sprintf(
			"title=%s;version=%s;desc=%s;httpPrefix=%s",
			api.Title,
			api.Version,
			api.Description,
			httpPrefix,
		)
	}

	for _, ut := range goaexpr.Root.Types {
		if ut == nil {
			continue
		}
		out.Declarations["Type:"+ut.Name()] = schemaJSON(api, ut.Attribute(), "type."+ut.Name())
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
		var svcHTTPPaths []string
		if httpSvc != nil {
			svcHTTPPaths = slices.Clone(httpSvc.Paths)
			slices.Sort(svcHTTPPaths)
		}
		out.Declarations["Service:"+svc.Name] = fmt.Sprintf(
			"desc=%s;httpPaths=%s;security=%s;errors=%s",
			svc.Description,
			strings.Join(svcHTTPPaths, ","),
			describeSecurity(svc.Requirements),
			describeErrors(api, svc.Errors),
		)

		for _, m := range svc.Methods {
			if m == nil {
				continue
			}
			var routes, respCodes, errCodes []string
			if httpSvc != nil {
				if ep := httpSvc.Endpoint(m.Name); ep != nil {
					for _, r := range ep.Routes {
						for _, fp := range r.FullPaths() {
							routes = append(routes, r.Method+" "+fp)
						}
					}
					for _, resp := range ep.Responses {
						respCodes = append(respCodes, fmt.Sprintf("%d", resp.StatusCode))
					}
					for _, he := range ep.HTTPErrors {
						if he.Response != nil {
							errCodes = append(errCodes, fmt.Sprintf("%s:%d", he.ErrorExpr.Name, he.Response.StatusCode))
						}
					}
				}
			}
			var grpcSig string
			if grpcSvc != nil {
				if gep := grpcSvc.Endpoint(m.Name); gep != nil {
					grpcSig = fmt.Sprintf(
						"req=%s;resp=%s",
						schemaJSON(api, gep.Request, "grpc.req."+svc.Name+"."+m.Name),
						schemaJSON(api, gep.Response.Message, "grpc.resp."+svc.Name+"."+m.Name),
					)
				}
			}
			slices.Sort(routes)
			slices.Sort(respCodes)
			slices.Sort(errCodes)
			key := fmt.Sprintf("Service:%s/Method:%s", svc.Name, m.Name)
			out.Declarations[key] = fmt.Sprintf(
				"desc=%s;payload=%s;result=%s;security=%s;errors=%s;routes=%s;status=%s;httpErrors=%s;grpc=%s",
				m.Description,
				schemaJSON(api, m.Payload, "method.payload."+svc.Name+"."+m.Name),
				schemaJSON(api, m.Result, "method.result."+svc.Name+"."+m.Name),
				describeSecurity(m.Requirements),
				describeErrors(api, m.Errors),
				strings.Join(routes, ","),
				strings.Join(respCodes, ","),
				strings.Join(errCodes, ","),
				grpcSig,
			)
		}

		if mcpexpr.Root != nil {
			if mcp := mcpexpr.Root.GetMCP(svc); mcp != nil {
				mcpKey := fmt.Sprintf("Service:%s/MCP:%s", svc.Name, mcp.Name)
				out.Declarations[mcpKey] = fmt.Sprintf("version=%s", mcp.Version)
				for _, t := range mcp.Tools {
					if t != nil && t.Method != nil {
						out.Declarations[fmt.Sprintf("%s/Tool:%s", mcpKey, t.Name)] = fmt.Sprintf("method=%s;desc=%s;ui=%s", t.Method.Name, t.Description, t.UIResourceURI)
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

		var inputBinds, fieldBinds []string
		for _, ib := range ts.InputBindings {
			if ib != nil {
				fields := slices.Clone(ib.Fields)
				slices.Sort(fields)
				inputBinds = append(inputBinds, ib.Tool+":"+strings.Join(fields, ","))
			}
		}
		for _, fb := range ts.FieldBindings {
			if fb != nil {
				fieldBinds = append(fieldBinds, fb.Field)
			}
		}
		slices.Sort(inputBinds)
		slices.Sort(fieldBinds)
		deferredTools := slices.Clone(ts.DeferredTools)
		slices.Sort(deferredTools)

		out.Declarations[tsDeclKey] = fmt.Sprintf(
			"desc=%s;deferred=%t;deferredTools=%s;inputBinds=%s;fieldBinds=%s",
			origin.Description,
			ts.Deferred,
			strings.Join(deferredTools, ","),
			strings.Join(inputBinds, ";"),
			strings.Join(fieldBinds, ","),
		)

		for _, t := range origin.Tools {
			if t == nil {
				continue
			}
			hidden := collectHiddenFields(t, ts)
			modelArgs := stripHiddenFields(t.Args, hidden)
			argsSig := schemaJSON(api, modelArgs, "tool.args."+origin.Name+"."+t.Name)
			retSig := schemaJSON(api, t.Return, "tool.return."+origin.Name+"."+t.Name)
			hasConfirm := t.Confirmation != nil
			var confirmSig string
			if t.Confirmation != nil {
				confirmSig = fmt.Sprintf("%s|%s|%s", t.Confirmation.Title, t.Confirmation.PromptTemplate, t.Confirmation.DeniedResultTemplate)
			}
			hasBounds := t.Bounds != nil
			var boundsSig string
			if t.Bounds != nil && t.Bounds.Paging != nil {
				boundsSig = fmt.Sprintf("%s|%s|%s", t.Bounds.Paging.CursorField, t.Bounds.Paging.NextCursorField, t.Bounds.Paging.ContinueTool)
			}
			toolKey := fmt.Sprintf("%s/Tool:%s", tsDeclKey, t.Name)
			out.Declarations[toolKey] = fmt.Sprintf(
				"desc=%s;args=%s;return=%s;hidden=%s;confirm=%s;bounds=%s;terminal=%t;bookkeeping=%t",
				t.Description,
				argsSig,
				retSig,
				strings.Join(hidden, ","),
				confirmSig,
				boundsSig,
				t.TerminalRun,
				t.Bookkeeping,
			)
			baseToolID := origin.Name + "." + t.Name
			tc := toolContract{
				ID:                   baseToolID,
				Toolset:              origin.Name,
				Name:                 t.Name,
				Description:          t.Description,
				ArgsSchema:           argsSig,
				ReturnSchema:         retSig,
				InjectedFields:       hidden,
				RequiresConfirmation: hasConfirm,
				BoundedResult:        hasBounds,
			}
			if idx, exists := toolIndexByID[baseToolID]; !exists {
				toolIndexByID[baseToolID] = len(out.Tools)
				out.Tools = append(out.Tools, tc)
			} else if len(ts.InputBindings) > 0 || len(ts.FieldBindings) > 0 {
				out.Tools[idx] = tc
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
					schemaJSON(api, sc.Input, "scenario.input."+suite.Name+"."+sc.Name),
				)
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

func collectHiddenFields(t *agentexpr.ToolExpr, consumer *agentexpr.ToolsetExpr) []string {
	seen := make(map[string]bool)
	for _, f := range t.InjectedFields {
		seen[f] = true
	}
	if consumer != nil {
		for _, ib := range consumer.InputBindings {
			if ib != nil && ib.Tool == t.Name {
				for _, f := range ib.Fields {
					seen[f] = true
				}
			}
		}
		for _, fb := range consumer.FieldBindings {
			if fb != nil {
				seen[fb.Field] = true
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

func stripHiddenFields(att *goaexpr.AttributeExpr, hidden []string) *goaexpr.AttributeExpr {
	if att == nil || len(hidden) == 0 {
		return att
	}
	cloned := goaexpr.DupAtt(att)
	obj := goaexpr.AsObject(cloned.Type)
	if obj == nil {
		return cloned
	}
	for _, field := range hidden {
		obj.Delete(field)
		if cloned.Validation != nil && len(cloned.Validation.Required) > 0 {
			cloned.Validation.Required = slices.DeleteFunc(cloned.Validation.Required, func(req string) bool {
				return req == field
			})
		}
	}
	return cloned
}

func describeSecurity(reqs []*goaexpr.SecurityExpr) string {
	if len(reqs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(reqs))
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
		parts = append(parts, strings.Join(schemes, "+")+"["+strings.Join(scopes, ",")+"]")
	}
	slices.Sort(parts)
	return strings.Join(parts, ";")
}

func describeErrors(api *goaexpr.APIExpr, errs []*goaexpr.ErrorExpr) string {
	if len(errs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		if e != nil {
			parts = append(parts, e.Name+":"+schemaJSON(api, e.AttributeExpr, "err."+e.Name))
		}
	}
	slices.Sort(parts)
	return strings.Join(parts, ";")
}
`
)

var (
	// resolveToolchainEnv caches the resolved GOROOT, GOMODCACHE, and private
	// sandboxed build cache directory used by candidate.Build.
	resolveToolchainEnv = sync.OnceValues(func() (toolchainEnv, error) {
		out, err := exec.Command("go", "env", "GOROOT", "GOMODCACHE").Output()
		if err != nil {
			return toolchainEnv{}, fmt.Errorf("resolve go env GOROOT GOMODCACHE: %w", err)
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) < 2 || strings.TrimSpace(lines[0]) == "" || strings.TrimSpace(lines[1]) == "" {
			return toolchainEnv{}, fmt.Errorf("unexpected go env output %q", string(out))
		}
		workDir, err := os.MkdirTemp("", "scantle-designeval-work-*")
		if err != nil {
			return toolchainEnv{}, fmt.Errorf("create designeval work dir: %w", err)
		}
		return toolchainEnv{
			goroot:   strings.TrimSpace(lines[0]),
			modCache: strings.TrimSpace(lines[1]),
			workDir:  workDir,
		}, nil
	})
)

// Evaluate compiles a standalone Goa/Goa-AI design evaluator binary inside
// verify/candidate.Build's OS-enforced build sandbox and runs it inside
// verify/candidate.Exec's OS-enforced single-process runtime sandbox, returning
// the evaluated Result.
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

	normalizedSrc, err := normalizeDesignPackage(designSource)
	if err != nil {
		return Result{}, fmt.Errorf("parse design source: %w", err)
	}

	modRoot, modPath, err := locateModuleRoot()
	if err != nil {
		return Result{}, err
	}
	tc, err := resolveToolchainEnv()
	if err != nil {
		return Result{}, err
	}

	tmpDir, err := os.MkdirTemp("", "scantle-designeval-*")
	if err != nil {
		return Result{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() {
		if rmErr := os.RemoveAll(tmpDir); rmErr != nil {
			return
		}
	}()

	buildDir := filepath.Join(tmpDir, "build")
	outDir := filepath.Join(tmpDir, "out")
	storeDir := filepath.Join(tmpDir, "store")
	stateDir := filepath.Join(tmpDir, "state")
	for _, d := range []string{buildDir, outDir, storeDir, stateDir, filepath.Join(buildDir, "design")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return Result{}, fmt.Errorf("mkdir %q: %w", d, err)
		}
	}

	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(modRoot, name))
		if err != nil {
			return Result{}, fmt.Errorf("read %s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(buildDir, name), data, 0o600); err != nil {
			return Result{}, fmt.Errorf("write %s: %w", name, err)
		}
	}

	if err := os.WriteFile(filepath.Join(buildDir, "design", "design.go"), []byte(normalizedSrc), 0o600); err != nil {
		return Result{}, fmt.Errorf("write design.go: %w", err)
	}
	harnessMain := strings.ReplaceAll(harnessMainTemplate, "{{DESIGN_IMPORT}}", strconv.Quote(modPath+"/design"))
	if err := os.WriteFile(filepath.Join(buildDir, "main.go"), []byte(harnessMain), 0o600); err != nil {
		return Result{}, fmt.Errorf("write evaluator main.go: %w", err)
	}

	rawBin := filepath.Join(outDir, "designeval-bin")
	buildLog := filepath.Join(tmpDir, "build.log")
	if err := candidate.Build(ctx, candidate.BuildSpec{
		ModuleDir: buildDir,
		Package:   ".",
		Out:       rawBin,
		GOROOT:    tc.goroot,
		ModCache:  tc.modCache,
		WorkDir:   tc.workDir,
		LogFile:   buildLog,
	}); err != nil {
		logBytes, readErr := os.ReadFile(buildLog)
		if readErr != nil {
			return Result{}, fmt.Errorf("compile Goa design: %w", err)
		}
		return Result{}, fmt.Errorf("compile Goa design: %w (%s)", err, strings.TrimSpace(string(logBytes)))
	}

	art, err := candidate.Pin(storeDir, rawBin)
	if err != nil {
		return Result{}, fmt.Errorf("pin design evaluator binary: %w", err)
	}
	defer func() {
		if rmErr := candidate.RemoveStore(storeDir); rmErr != nil {
			return
		}
	}()

	outFile := filepath.Join(stateDir, "evaluated.json")
	logFile := filepath.Join(tmpDir, "designeval.log")
	proc, err := candidate.Exec(ctx, art, candidate.Spec{
		Args:     []string{"-out", outFile},
		StateDir: stateDir,
		LogFile:  logFile,
	})
	if err != nil {
		return Result{}, fmt.Errorf("exec sandboxed design evaluator: %w", err)
	}
	if waitErr := proc.Wait(); waitErr != nil {
		excerpt, _, exErr := proc.Excerpt(16 << 10)
		if exErr != nil {
			return Result{}, fmt.Errorf("evaluate Goa design in sandbox: %w", waitErr)
		}
		return Result{}, fmt.Errorf("evaluate Goa design in sandbox: %w (%s)", waitErr, strings.TrimSpace(excerpt))
	}

	rawJSON, err := os.ReadFile(outFile)
	if err != nil {
		return Result{}, fmt.Errorf("read evaluated design output: %w", err)
	}
	var res Result
	if err := json.Unmarshal(rawJSON, &res); err != nil {
		return Result{}, fmt.Errorf("decode evaluated Goa design: %w", err)
	}
	return res, nil
}

// normalizeDesignPackage parses src as Go source and ensures its package clause
// is `package design` so the temporary evaluator harness can import it cleanly.
func normalizeDesignPackage(src string) (string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "design.go", src, parser.ParseComments)
	if err != nil {
		return "", err
	}
	if file.Name != nil && file.Name.Name != "design" {
		file.Name = ast.NewIdent("design")
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, fset, file); err != nil {
			return "", err
		}
		return buf.String(), nil
	}
	return src, nil
}

// locateModuleRoot resolves the repository root containing go.mod and returns
// its directory path and declared module path.
func locateModuleRoot() (string, string, error) {
	var candidates []string
	if envRoot := strings.TrimSpace(os.Getenv("SCANTLE_MODULE_ROOT")); envRoot != "" {
		candidates = append(candidates, envRoot)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		candidates = append(candidates, filepath.Dir(file))
	}

	for _, start := range candidates {
		dir := start
		for {
			modFile := filepath.Join(dir, "go.mod")
			if raw, err := os.ReadFile(modFile); err == nil {
				if modPath := modfile.ModulePath(raw); modPath != "" {
					return dir, modPath, nil
				}
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return "", "", errors.New("go.mod not found via SCANTLE_MODULE_ROOT, working directory, or caller path")
}

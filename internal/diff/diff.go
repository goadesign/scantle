// Package diff computes structural AST and module dependency diffs for Goa
// designs and go.mod files without emitting a blanket compatibility verdict.
package diff

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"
)

type (
	// Report summarizes the structural Goa design declarations and go.mod
	// directives that changed between two snapshots.
	Report struct {
		// HasChanges is true when at least one declaration, contract body, or module directive changed.
		HasChanges bool
		// AddedDeclarations lists newly introduced scoped Goa design declarations.
		AddedDeclarations []string
		// RemovedDeclarations lists deleted scoped Goa design declarations.
		RemovedDeclarations []string
		// ModifiedDeclarations lists scoped Goa declarations whose contract body changed.
		ModifiedDeclarations []string
		// VersionDeltas lists changed, added, or removed go.mod require, replace, or exclude directives.
		VersionDeltas []string
		// Summary is a factual human-readable summary of the structural changes.
		Summary string
	}
)

var (
	// dslCalls lists the Goa and Goa-AI DSL call names tracked as scoped declarations.
	dslCalls = map[string]bool{
		"API":      true,
		"Service":  true,
		"Method":   true,
		"Agent":    true,
		"Toolset":  true,
		"Use":      true,
		"Tool":     true,
		"Resource": true,
		"MCP":      true,
		"Suite":    true,
		"Scenario": true,
		"Type":     true,
	}
)

// Compare parses old and new Go/Goa design sources and go.mod files and returns
// a factual structural diff Report.
func Compare(oldDesign, newDesign, oldGoMod, newGoMod string) (Report, error) {
	oldDecls, err := parseDeclarations(oldDesign)
	if err != nil {
		return Report{}, fmt.Errorf("parse old design: %w", err)
	}
	newDecls, err := parseDeclarations(newDesign)
	if err != nil {
		return Report{}, fmt.Errorf("parse new design: %w", err)
	}
	deltas, err := compareGoMod(oldGoMod, newGoMod)
	if err != nil {
		return Report{}, err
	}

	added, removed, modified := diffDeclarations(oldDecls, newDecls)
	hasChanges := len(added) > 0 || len(removed) > 0 || len(modified) > 0 || len(deltas) > 0

	summary := "No structural design or module dependency changes."
	if hasChanges {
		parts := make([]string, 0, 4)
		if len(added) > 0 {
			parts = append(parts, fmt.Sprintf("added %d declaration(s) (%s)", len(added), strings.Join(added, ", ")))
		}
		if len(removed) > 0 {
			parts = append(parts, fmt.Sprintf("removed %d declaration(s) (%s)", len(removed), strings.Join(removed, ", ")))
		}
		if len(modified) > 0 {
			parts = append(parts, fmt.Sprintf("modified %d declaration(s) (%s)", len(modified), strings.Join(modified, ", ")))
		}
		if len(deltas) > 0 {
			parts = append(parts, fmt.Sprintf("updated %d module directive(s) (%s)", len(deltas), strings.Join(deltas, ", ")))
		}
		summary = "Structural diff: " + strings.Join(parts, "; ") + "."
	}

	return Report{
		HasChanges:           hasChanges,
		AddedDeclarations:    added,
		RemovedDeclarations:  removed,
		ModifiedDeclarations: modified,
		VersionDeltas:        deltas,
		Summary:              summary,
	}, nil
}

// parseDeclarations extracts scoped `<Kind>:<Name>` identifiers mapped to their
// canonical non-child AST body signature from a Go/Goa design source string.
func parseDeclarations(src string) (map[string]string, error) {
	decls := make(map[string]string)
	if strings.TrimSpace(src) == "" {
		return decls, nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "design.go", src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	walkNode(file, nil, decls)
	return decls, nil
}

// walkNode traverses n with the active declaration scope stack and populates decls.
func walkNode(n ast.Node, scope []string, decls map[string]string) {
	if n == nil {
		return
	}
	if call, ok := n.(*ast.CallExpr); ok {
		if seg, isTracked := trackedCallSegment(call); isTracked {
			nextScope := append(slices.Clone(scope), seg)
			key := strings.Join(nextScope, "/")
			var sig strings.Builder
			for i, arg := range call.Args {
				if i > 0 {
					sig.WriteByte(',')
				}
				writeBodySignature(&sig, arg)
			}
			decls[key] = sig.String()
			for _, arg := range call.Args {
				walkNode(arg, nextScope, decls)
			}
			return
		}
	}
	ast.Inspect(n, func(child ast.Node) bool {
		if child == nil || child == n {
			return true
		}
		if call, ok := child.(*ast.CallExpr); ok {
			if _, isTracked := trackedCallSegment(call); isTracked {
				walkNode(call, scope, decls)
				return false
			}
		}
		return true
	})
}

// trackedCallSegment returns `<Kind>:<Name>` when call is a tracked Goa/Goa-AI
// declaration with a string literal or identifier name in Args[0].
func trackedCallSegment(call *ast.CallExpr) (string, bool) {
	if call == nil || len(call.Args) == 0 {
		return "", false
	}
	name := callIdent(call.Fun)
	if !dslCalls[name] {
		return "", false
	}
	label := extractLabel(call.Args[0])
	if label == "" {
		return "", false
	}
	return name + ":" + label, true
}

// extractLabel extracts a declaration label from a string literal or identifier.
func extractLabel(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			return strings.Trim(v.Value, "\"`")
		}
	case *ast.Ident:
		return v.Name
	}
	return ""
}

// writeBodySignature serializes the canonical AST structure of node into b while
// skipping nested tracked declarations (which have their own keys in decls).
func writeBodySignature(b *strings.Builder, node ast.Node) {
	if node == nil {
		return
	}
	switch v := node.(type) {
	case *ast.CallExpr:
		if _, isTracked := trackedCallSegment(v); isTracked {
			return
		}
		b.WriteString("Call(")
		writeBodySignature(b, v.Fun)
		for _, arg := range v.Args {
			b.WriteByte(',')
			writeBodySignature(b, arg)
		}
		b.WriteByte(')')
	case *ast.FuncLit:
		b.WriteString("Func(")
		if v.Body != nil {
			for _, stmt := range v.Body.List {
				writeBodySignature(b, stmt)
			}
		}
		b.WriteByte(')')
	case *ast.ExprStmt:
		writeBodySignature(b, v.X)
	case *ast.AssignStmt:
		b.WriteString("Assign(")
		for _, lhs := range v.Lhs {
			writeBodySignature(b, lhs)
		}
		b.WriteByte('=')
		for _, rhs := range v.Rhs {
			writeBodySignature(b, rhs)
		}
		b.WriteByte(')')
	case *ast.BlockStmt:
		for _, stmt := range v.List {
			writeBodySignature(b, stmt)
		}
	case *ast.Ident:
		b.WriteString("Ident(")
		b.WriteString(v.Name)
		b.WriteByte(')')
	case *ast.BasicLit:
		b.WriteString("Lit(")
		b.WriteString(v.Value)
		b.WriteByte(')')
	case *ast.SelectorExpr:
		writeBodySignature(b, v.X)
		b.WriteByte('.')
		writeBodySignature(b, v.Sel)
	case *ast.UnaryExpr:
		b.WriteString(v.Op.String())
		writeBodySignature(b, v.X)
	case *ast.BinaryExpr:
		b.WriteByte('(')
		writeBodySignature(b, v.X)
		b.WriteString(v.Op.String())
		writeBodySignature(b, v.Y)
		b.WriteByte(')')
	case *ast.KeyValueExpr:
		writeBodySignature(b, v.Key)
		b.WriteByte(':')
		writeBodySignature(b, v.Value)
	case *ast.CompositeLit:
		writeBodySignature(b, v.Type)
		b.WriteByte('{')
		for _, elt := range v.Elts {
			writeBodySignature(b, elt)
			b.WriteByte(';')
		}
		b.WriteByte('}')
	default:
		ast.Inspect(node, func(child ast.Node) bool {
			if child == nil || child == node {
				return true
			}
			writeBodySignature(b, child)
			return false
		})
	}
}

// callIdent returns the unqualified function name for an AST call expression.
func callIdent(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	default:
		return ""
	}
}

// diffDeclarations partitions old and new declaration maps into sorted added,
// removed, and modified declaration slices.
func diffDeclarations(oldDecls, newDecls map[string]string) (added, removed, modified []string) {
	added = make([]string, 0)
	removed = make([]string, 0)
	modified = make([]string, 0)
	for k, newSig := range newDecls {
		oldSig, hadOld := oldDecls[k]
		if !hadOld {
			added = append(added, k)
		} else if oldSig != newSig {
			modified = append(modified, k)
		}
	}
	for k := range oldDecls {
		if _, hasNew := newDecls[k]; !hasNew {
			removed = append(removed, k)
		}
	}
	slices.Sort(added)
	slices.Sort(removed)
	slices.Sort(modified)
	return added, removed, modified
}

// compareGoMod parses two go.mod files using golang.org/x/mod/modfile and
// returns sorted human-readable deltas across require, replace, and exclude
// directives.
func compareGoMod(oldMod, newMod string) ([]string, error) {
	oldPins, err := parseGoModDirectives(oldMod)
	if err != nil {
		return nil, fmt.Errorf("parse old go.mod: %w", err)
	}
	newPins, err := parseGoModDirectives(newMod)
	if err != nil {
		return nil, fmt.Errorf("parse new go.mod: %w", err)
	}
	seen := make(map[string]bool, len(oldPins)+len(newPins))
	for k := range oldPins {
		seen[k] = true
	}
	for k := range newPins {
		seen[k] = true
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	deltas := make([]string, 0)
	for _, key := range keys {
		oldVer, hadOld := oldPins[key]
		newVer, hasNew := newPins[key]
		switch {
		case !hadOld && hasNew:
			deltas = append(deltas, fmt.Sprintf("%s: added %s", key, newVer))
		case hadOld && !hasNew:
			deltas = append(deltas, fmt.Sprintf("%s: removed %s", key, oldVer))
		case oldVer != newVer:
			deltas = append(deltas, fmt.Sprintf("%s: %s -> %s", key, oldVer, newVer))
		}
	}
	return deltas, nil
}

// parseGoModDirectives parses require, replace, and exclude directives from a
// go.mod file string using golang.org/x/mod/modfile.
func parseGoModDirectives(raw string) (map[string]string, error) {
	pins := make(map[string]string)
	if strings.TrimSpace(raw) == "" {
		return pins, nil
	}
	parsed, err := modfile.Parse("go.mod", []byte(raw), nil)
	if err != nil {
		return nil, err
	}
	for _, req := range parsed.Require {
		if req == nil {
			continue
		}
		pins[req.Mod.Path] = req.Mod.Version
	}
	for _, rep := range parsed.Replace {
		if rep == nil {
			continue
		}
		oldKey := "replace " + rep.Old.Path
		if rep.Old.Version != "" {
			oldKey += "@" + rep.Old.Version
		}
		target := rep.New.Path
		if rep.New.Version != "" {
			target += " " + rep.New.Version
		}
		pins[oldKey] = target
	}
	for _, exc := range parsed.Exclude {
		if exc == nil {
			continue
		}
		pins["exclude "+exc.Mod.Path] = exc.Mod.Version
	}
	return pins, nil
}

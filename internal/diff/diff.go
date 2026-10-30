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
)

type (
	// Report summarizes the structural Goa design declarations and go.mod
	// dependency pins that changed between two snapshots.
	Report struct {
		// HasChanges is true when at least one declaration or module version changed.
		HasChanges bool
		// AddedDeclarations lists newly introduced Goa design declarations.
		AddedDeclarations []string
		// RemovedDeclarations lists deleted Goa design declarations.
		RemovedDeclarations []string
		// VersionDeltas lists changed, added, or removed go.mod dependency pins.
		VersionDeltas []string
		// Summary is a factual human-readable summary of the structural changes.
		Summary string
	}
)

var (
	// dslCalls lists the Goa and Goa-AI DSL call names tracked in structural diffs.
	dslCalls = map[string]bool{
		"API":      true,
		"Service":  true,
		"Method":   true,
		"Agent":    true,
		"Toolset":  true,
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

	added := setDifference(newDecls, oldDecls)
	removed := setDifference(oldDecls, newDecls)
	deltas := compareGoMod(oldGoMod, newGoMod)
	hasChanges := len(added) > 0 || len(removed) > 0 || len(deltas) > 0

	summary := "No structural design or module dependency changes."
	if hasChanges {
		parts := make([]string, 0, 3)
		if len(added) > 0 {
			parts = append(parts, fmt.Sprintf("added %d declaration(s) (%s)", len(added), strings.Join(added, ", ")))
		}
		if len(removed) > 0 {
			parts = append(parts, fmt.Sprintf("removed %d declaration(s) (%s)", len(removed), strings.Join(removed, ", ")))
		}
		if len(deltas) > 0 {
			parts = append(parts, fmt.Sprintf("updated %d module pin(s) (%s)", len(deltas), strings.Join(deltas, ", ")))
		}
		summary = "Structural diff: " + strings.Join(parts, "; ") + "."
	}

	return Report{
		HasChanges:          hasChanges,
		AddedDeclarations:   added,
		RemovedDeclarations: removed,
		VersionDeltas:       deltas,
		Summary:             summary,
	}, nil
}

// parseDeclarations extracts sorted `<Kind>:<Name>` identifiers from a Go/Goa
// design source string.
func parseDeclarations(src string) (map[string]bool, error) {
	decls := make(map[string]bool)
	if strings.TrimSpace(src) == "" {
		return decls, nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "design.go", src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		name := callIdent(call.Fun)
		if !dslCalls[name] {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		label := strings.Trim(lit.Value, "\"`")
		if label != "" {
			decls[name+":"+label] = true
		}
		return true
	})
	return decls, nil
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

// setDifference returns the sorted keys present in left but absent in right.
func setDifference(left, right map[string]bool) []string {
	out := make([]string, 0)
	for k := range left {
		if !right[k] {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// compareGoMod extracts `module version` pins from two go.mod contents and
// returns sorted human-readable deltas.
func compareGoMod(oldMod, newMod string) []string {
	oldPins := parseGoModPins(oldMod)
	newPins := parseGoModPins(newMod)
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
	for _, mod := range keys {
		oldVer, hadOld := oldPins[mod]
		newVer, hasNew := newPins[mod]
		switch {
		case !hadOld && hasNew:
			deltas = append(deltas, fmt.Sprintf("%s: added %s", mod, newVer))
		case hadOld && !hasNew:
			deltas = append(deltas, fmt.Sprintf("%s: removed %s", mod, oldVer))
		case oldVer != newVer:
			deltas = append(deltas, fmt.Sprintf("%s: %s -> %s", mod, oldVer, newVer))
		}
	}
	return deltas
}

// parseGoModPins parses module requirement lines from a go.mod file.
func parseGoModPins(raw string) map[string]string {
	pins := make(map[string]string)
	inRequireBlock := false
	for _, line := range strings.Split(raw, "\n") {
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = line[:idx]
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "require (") {
			inRequireBlock = true
			continue
		}
		if inRequireBlock && trimmed == ")" {
			inRequireBlock = false
			continue
		}
		if strings.HasPrefix(trimmed, "require ") {
			fields := strings.Fields(strings.TrimPrefix(trimmed, "require "))
			if len(fields) >= 2 {
				pins[fields[0]] = fields[1]
			}
			continue
		}
		if inRequireBlock {
			fields := strings.Fields(trimmed)
			if len(fields) >= 2 {
				pins[fields[0]] = fields[1]
			}
		}
	}
	return pins
}

// Package surla holds the go.mod Surla was built from, so code that needs
// Surla's own dependency versions reads them from the binary instead of
// looking for a go.mod on disk.
package surla

import _ "embed"

// goMod is the content of Surla's go.mod at build time.
//
//go:embed go.mod
var goMod string

// GoMod returns the go.mod Surla was built from. The design evaluator
// (internal/designeval) reads the Goa, Goa-AI and Model versions it requires
// as the generator versions for designs that do not require them. Because the
// file is compiled in, the result does not depend on the working directory or
// on a source checkout being present.
func GoMod() string {
	return goMod
}

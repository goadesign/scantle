// This file holds the promotion target `surla serve -promotion-config`
// binds: it approves staged release binaries for a fresh launch.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/goadesign/surla/internal/controlplane"
	"github.com/goadesign/surla/internal/verify"
	"github.com/goadesign/surla/internal/verifyrun"
)

type (
	// promotionFile is the operator's CP5 configuration read from
	// -promotion-config. Every field is required.
	promotionFile struct {
		// ReportDir holds the full reports the evaluator wrote, named by
		// digest; it is the report_dir verification writes to.
		ReportDir string `json:"report_dir"`
		// ReleaseDir holds one directory per candidate commit, each holding
		// the binaries to deploy for that commit.
		ReleaseDir string `json:"release_dir"`
		// SuiteID is the evaluator suite a promotion requires.
		SuiteID string `json:"suite_id"`
		// SuiteCommit is the evaluator suite commit a promotion requires.
		SuiteCommit string `json:"suite_commit"`
		// Environment is the dependency profile a promotion requires.
		Environment string `json:"environment"`
		// Binaries names the binaries every release directory must hold.
		Binaries []string `json:"binaries"`
	}

	// releaseTarget approves the binaries staged for a fresh launch: the
	// files in <release_dir>/<commit>/, which the operator's deploy step
	// starts as new processes with the evaluator suite's fixed
	// configuration. It says nothing about processes already running, which
	// may load runtime prompt overrides no report covers. The operator
	// publishes a complete commit directory in one step (for example by
	// renaming a fully written directory into release_dir) and never changes
	// its files in place.
	releaseTarget struct {
		// cfg is the validated configuration.
		cfg promotionFile
	}
)

// CurrentInputs returns the required suite and environment and the SHA-256
// digest of each configured binary in <release_dir>/<candidateCommit>/. It
// opens that directory once and reads every binary through it, so the
// digests come from one directory even if the operator publishes a
// replacement meanwhile; a later replacement makes this answer stale, as any
// answer can become. A missing or unreadable binary fails the call.
func (t *releaseTarget) CurrentInputs(_ context.Context, candidateCommit string) (controlplane.PromotionInputs, error) {
	dir := filepath.Join(t.cfg.ReleaseDir, candidateCommit)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return controlplane.PromotionInputs{}, fmt.Errorf("open release directory for commit %s: %w", candidateCommit, err)
	}
	artifacts := make(map[string]string, len(t.cfg.Binaries))
	for _, name := range t.cfg.Binaries {
		digest, err := hashFile(root, name)
		if err != nil {
			return controlplane.PromotionInputs{}, errors.Join(
				fmt.Errorf("read release binary %s for commit %s: %w", name, candidateCommit, err), root.Close())
		}
		artifacts[name] = digest
	}
	if err := root.Close(); err != nil {
		return controlplane.PromotionInputs{}, fmt.Errorf("close release directory for commit %s: %w", candidateCommit, err)
	}
	return controlplane.PromotionInputs{
		SuiteID:     t.cfg.SuiteID,
		SuiteCommit: t.cfg.SuiteCommit,
		Environment: t.cfg.Environment,
		Artifacts:   artifacts,
	}, nil
}

// LoadReport returns the full report the evaluator saved under digest in
// report_dir. approve_merge's payload validation guarantees digest is 64
// lowercase hex characters.
func (t *releaseTarget) LoadReport(_ context.Context, digest string) (verify.Report, error) {
	return verifyrun.LoadReport(t.cfg.ReportDir, digest)
}

// loadPromotionConfig reads and validates -promotion-config. An empty path
// means approve_merge stays unavailable; any missing field, unknown field,
// trailing data, or binary name that is not a single distinct file name
// stops startup.
func loadPromotionConfig(path string) (controlplane.PromotionTarget, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read -promotion-config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var f promotionFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("decode -promotion-config %s: %w", path, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode -promotion-config %s: data after the configuration", path)
	}
	var missing []string
	for _, field := range []struct{ name, value string }{
		{"report_dir", f.ReportDir}, {"release_dir", f.ReleaseDir}, {"suite_id", f.SuiteID},
		{"suite_commit", f.SuiteCommit}, {"environment", f.Environment},
	} {
		if field.value == "" {
			missing = append(missing, field.name)
		}
	}
	if len(f.Binaries) == 0 {
		missing = append(missing, "binaries")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("-promotion-config %s is missing %s", path, strings.Join(missing, ", "))
	}
	seen := make(map[string]bool, len(f.Binaries))
	for _, name := range f.Binaries {
		if name == "" || name == "." || name == ".." || strings.ContainsRune(name, filepath.Separator) || seen[name] {
			return nil, fmt.Errorf("-promotion-config %s: binary %q must be a distinct file name", path, name)
		}
		seen[name] = true
	}
	return &releaseTarget{cfg: f}, nil
}

// hashFile returns the hex SHA-256 digest of name inside root.
func hashFile(root *os.Root, name string) (string, error) {
	f, err := root.Open(name)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	if err := errors.Join(err, f.Close()); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

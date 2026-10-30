// Package verify turns an evaluator suite's eval.Report into Scantle's CP4
// verification report and checks, at CP5, that the report still describes the
// change being promoted.
//
// The report is bound to the exact inputs that were tested: the candidate
// commit and request revision, the evaluator suite and its commit, the digest
// of every candidate binary the suite actually executed, the environment
// profile, and the configuration digest. Every scenario the suite declares is
// accounted for in the denominator, and holdout scenarios are reduced to counts
// in the view returned to builders.
package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"goa.design/goa-ai/eval"
)

type (
	// Binding is the exact set of inputs a verification report describes.
	Binding struct {
		// CandidateCommit is the commit the candidate binaries were built
		// from.
		CandidateCommit string `json:"candidate_commit"`
		// RequestRevision identifies the confirmed request (CP1) the change
		// implements.
		RequestRevision string `json:"request_revision"`
		// SuiteID names the evaluator suite.
		SuiteID string `json:"suite_id"`
		// SuiteCommit is the commit of the evaluator-owned suite.
		SuiteCommit string `json:"suite_commit"`
		// Artifacts maps each candidate binary name to the SHA-256 digest of
		// the binary verify built and expects the suite to execute.
		Artifacts map[string]string `json:"artifacts"`
		// Environment names the dependency profile, such as "sim".
		Environment string `json:"environment"`
		// ConfigDigest identifies the candidate's effective configuration,
		// including resolved prompt versions.
		ConfigDigest string `json:"config_digest"`
	}

	// Suite describes the scenarios the evaluator declares. It comes from the
	// evaluator's own generated suite, never from the candidate.
	Suite struct {
		// ID is the suite identifier.
		ID string
		// Scenarios lists every declared scenario in declaration order.
		Scenarios []Scenario
	}

	// Scenario is one declared scenario.
	Scenario struct {
		// ID is the scenario identifier.
		ID string
		// Holdout marks scenarios whose details builders must never see.
		Holdout bool
	}

	// Outcome is the result of one required scenario.
	Outcome string

	// ScenarioResult is one scenario's outcome in the report.
	ScenarioResult struct {
		// ID is the scenario identifier.
		ID string `json:"id"`
		// Holdout marks a holdout scenario.
		Holdout bool `json:"holdout"`
		// Outcome is how the scenario ended.
		Outcome Outcome `json:"outcome"`
		// Detail is the evaluator's scenario report, kept for operators and
		// removed from the builder view for holdout scenarios.
		Detail *eval.ScenarioReport `json:"detail,omitempty"`
	}

	// Counts tallies scenario outcomes.
	Counts struct {
		// Required is the number of declared scenarios.
		Required int `json:"required"`
		// Passed counts scenarios whose checks all passed.
		Passed int `json:"passed"`
		// Failed counts scenarios that ran and failed a check or claim.
		Failed int `json:"failed"`
		// NotRun counts declared scenarios with no result.
		NotRun int `json:"not_run"`
		// HarnessError counts scenarios whose environment broke.
		HarnessError int `json:"harness_error"`
	}

	// Report is the CP4 verification report.
	Report struct {
		// Binding is what was tested.
		Binding Binding `json:"binding"`
		// Counts covers every declared scenario.
		Counts Counts `json:"counts"`
		// Scenarios lists every declared scenario in declaration order.
		Scenarios []ScenarioResult `json:"scenarios"`
		// Passed is true only when every declared scenario passed.
		Passed bool `json:"passed"`
		// Digest identifies this exact report: the SHA-256 of its canonical
		// JSON with Digest empty.
		Digest string `json:"digest"`
	}
)

const (
	// OutcomePassed means every check and claim of the scenario passed.
	OutcomePassed Outcome = "passed"
	// OutcomeFailed means the scenario ran and a check or claim failed.
	OutcomeFailed Outcome = "failed"
	// OutcomeNotRun means the suite produced no result for the scenario.
	OutcomeNotRun Outcome = "not_run"
	// OutcomeHarnessError means the scenario's environment broke before it
	// could judge the candidate.
	OutcomeHarnessError Outcome = "harness_error"

	// artifactPrefix marks a scenario artifact that records the digest of a
	// candidate binary the scenario executed: name "candidate:<binary>",
	// URI "sha256:<digest>".
	artifactPrefix = "candidate:"
)

// ErrStaleReport reports that a report no longer describes the change being
// promoted.
var ErrStaleReport = errors.New("verification report does not match the change being promoted")

// Assemble builds the CP4 report for binding from the suite's eval.Report.
// Each declared scenario counts once. A scenario passes only when the suite
// says it passed and every candidate binary it reports having executed is one
// of the binding's artifacts with the expected digest; a scenario that ran a
// different binary, or reported no binary at all, is a harness error.
func Assemble(binding Binding, suite Suite, evalReport eval.Report) (Report, error) {
	if evalReport.SuiteID != suite.ID || binding.SuiteID != suite.ID {
		return Report{}, fmt.Errorf("report suite %q, binding suite %q, declared suite %q differ",
			evalReport.SuiteID, binding.SuiteID, suite.ID)
	}
	byID := make(map[string]eval.ScenarioReport, len(evalReport.Scenarios))
	for _, s := range evalReport.Scenarios {
		byID[s.ID] = s
	}
	r := Report{Binding: binding, Counts: Counts{Required: len(suite.Scenarios)}}
	for _, declared := range suite.Scenarios {
		res := ScenarioResult{ID: declared.ID, Holdout: declared.Holdout}
		s, ok := byID[declared.ID]
		switch {
		case !ok:
			res.Outcome = OutcomeNotRun
		case s.StartedAt.IsZero():
			res.Outcome, res.Detail = OutcomeNotRun, &s
		case s.Error != "" || s.Result == nil:
			res.Outcome, res.Detail = OutcomeHarnessError, &s
		default:
			res.Detail = &s
			res.Outcome = OutcomeFailed
			if s.Passed {
				res.Outcome = OutcomePassed
			}
			if err := checkArtifacts(binding.Artifacts, s.Result.Artifacts); err != nil {
				res.Outcome = OutcomeHarnessError
				s.Error = err.Error()
			}
		}
		r.Counts.add(res.Outcome)
		r.Scenarios = append(r.Scenarios, res)
	}
	r.Passed = r.Counts.Passed == r.Counts.Required
	digest, err := r.computeDigest()
	if err != nil {
		return Report{}, err
	}
	r.Digest = digest
	return r, nil
}

// BuilderView returns the report as builders may see it: holdout scenarios keep
// their identity and outcome but lose every detail.
func (r Report) BuilderView() Report {
	view := r
	view.Scenarios = make([]ScenarioResult, len(r.Scenarios))
	for i, s := range r.Scenarios {
		if s.Holdout {
			s.ID = fmt.Sprintf("holdout-%d", i+1)
			s.Detail = nil
		}
		view.Scenarios[i] = s
	}
	return view
}

// Approve checks, at promotion time, that report still describes current: the
// report's digest must match its content, it must have passed, and every
// binding field must equal the change being promoted. Any difference fails
// with ErrStaleReport; the report stays readable but cannot authorize the
// changed operation.
func Approve(current Binding, report Report) error {
	digest, err := report.computeDigest()
	if err != nil {
		return err
	}
	if digest != report.Digest {
		return fmt.Errorf("%w: report content does not match its digest", ErrStaleReport)
	}
	if !report.Passed {
		return fmt.Errorf("%w: report did not pass", ErrStaleReport)
	}
	if diff := report.Binding.differences(current); len(diff) > 0 {
		return fmt.Errorf("%w: changed %s", ErrStaleReport, strings.Join(diff, ", "))
	}
	return nil
}

// checkArtifacts requires the scenario to report the digest of at least one
// candidate binary and every reported digest to match the binding.
func checkArtifacts(want map[string]string, reported []eval.Artifact) error {
	seen := 0
	for _, a := range reported {
		name, ok := strings.CutPrefix(a.Name, artifactPrefix)
		if !ok {
			continue
		}
		seen++
		digest, ok := strings.CutPrefix(a.URI, "sha256:")
		if !ok {
			return fmt.Errorf("artifact %s has no sha256 URI", a.Name)
		}
		expected, ok := want[name]
		if !ok {
			return fmt.Errorf("scenario executed unbound binary %q", name)
		}
		if digest != expected {
			return fmt.Errorf("scenario executed %s with digest %s, bound %s", name, digest, expected)
		}
	}
	if seen == 0 {
		return errors.New("scenario reported no candidate binary digest")
	}
	return nil
}

// computeDigest hashes the report's canonical JSON with Digest cleared.
func (r Report) computeDigest() (string, error) {
	r.Digest = ""
	b, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("encode report: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// add tallies one outcome.
func (c *Counts) add(o Outcome) {
	switch o {
	case OutcomePassed:
		c.Passed++
	case OutcomeFailed:
		c.Failed++
	case OutcomeNotRun:
		c.NotRun++
	case OutcomeHarnessError:
		c.HarnessError++
	}
}

// differences lists the binding fields that differ from other.
func (b Binding) differences(other Binding) []string {
	var diff []string
	if b.CandidateCommit != other.CandidateCommit {
		diff = append(diff, "candidate commit")
	}
	if b.RequestRevision != other.RequestRevision {
		diff = append(diff, "request revision")
	}
	if b.SuiteID != other.SuiteID || b.SuiteCommit != other.SuiteCommit {
		diff = append(diff, "evaluator suite")
	}
	if !maps.Equal(b.Artifacts, other.Artifacts) {
		diff = append(diff, "candidate binaries")
	}
	if b.Environment != other.Environment {
		diff = append(diff, "environment")
	}
	if b.ConfigDigest != other.ConfigDigest {
		diff = append(diff, "configuration")
	}
	return slices.Clip(diff)
}

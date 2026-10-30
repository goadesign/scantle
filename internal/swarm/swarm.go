// Package swarm plans deterministic Chorus multi-agent role assignments,
// capability- and cross-family model routing, and pinned collaboration
// manifests for Scantle builds.
//
// Chorus (`who`, `join`, `send`, `read`) owns real-time message routing,
// channel subscriptions, and pinned channel manifests. This package owns the
// server-side mapping from verified coding-agent provider profiles and explicit
// builder write scopes to proposed role assignments (`@architect`,
// `@builder-*`, `@eval-author`, and `@reviewer`). Role labels and cross-family
// model assignments never substitute for OS process and workspace isolation.
package swarm

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

type (
	// RoleKind identifies a specialized agent role in a Scantle build swarm.
	RoleKind string

	// ProviderProfile describes one server-configured coding-agent model profile.
	// Authentication, model family, and role capabilities come from server-owned
	// configuration, never from model-authored arguments or Chorus role strings.
	ProviderProfile struct {
		// ID is the unique profile identifier (for example, "claude-opus" or "codex-gpt5").
		ID string `json:"id"`
		// Provider names the backing CLI or model provider (for example, "anthropic", "openai", "google").
		Provider string `json:"provider"`
		// Model is the concrete model identifier (for example, "claude-opus-4-6", "gpt-5.2-codex").
		Model string `json:"model"`
		// Family is the canonical model family (for example, "claude", "gpt", "gemini").
		// Distinct profiles from the same vendor family share the same Family value.
		Family string `json:"family"`
		// Authenticated is true only when the server has verified usable credentials for this profile.
		Authenticated bool `json:"authenticated"`
		// Capabilities lists the Scantle swarm roles this profile can perform.
		Capabilities []RoleKind `json:"capabilities"`
	}

	// BuilderScope defines an explicit file-ownership slice for one builder agent.
	BuilderScope struct {
		// Name is the human-readable scope name (for example, "orders" or "api").
		Name string `json:"name"`
		// Paths lists the repository-relative path globs owned by this builder.
		Paths []string `json:"paths"`
		// PreferredProfileID optionally pins a specific authenticated profile ID for this builder.
		PreferredProfileID string `json:"preferred_profile_id,omitempty"`
	}

	// Request specifies the inputs for planning a Scantle build swarm roster.
	Request struct {
		// ChangeID is the Change Card identifier being planned.
		ChangeID string `json:"change_id"`
		// DesignDigest is the SHA-256 digest of the evaluated CP2 design snapshot.
		DesignDigest string `json:"design_digest"`
		// Profiles lists the server-configured coding-agent profiles.
		Profiles []ProviderProfile `json:"profiles"`
		// ArchitectPaths lists the paths owned by @architect (defaults to ["design/**"] when empty).
		ArchitectPaths []string `json:"architect_paths,omitempty"`
		// Builders lists the explicit builder write scopes (defaults to a single module builder when empty).
		Builders []BuilderScope `json:"builders,omitempty"`
		// EvalPaths lists the isolated evaluator workspace paths (defaults to ["*-eval/**"] when empty).
		EvalPaths []string `json:"eval_paths,omitempty"`
	}

	// Assignment describes one proposed agent role in the Scantle build swarm.
	Assignment struct {
		// Handle is the deterministic Chorus agent handle (without leading '@', <= 32 chars).
		Handle string `json:"handle"`
		// Role is the assigned RoleKind ("architect", "builder", "eval_author", or "reviewer").
		Role RoleKind `json:"role"`
		// ScopeName is the human-readable scope label for this assignment.
		ScopeName string `json:"scope_name"`
		// ProfileID is the assigned ProviderProfile.ID.
		ProfileID string `json:"profile_id"`
		// Provider is the assigned ProviderProfile.Provider.
		Provider string `json:"provider"`
		// Model is the assigned ProviderProfile.Model.
		Model string `json:"model"`
		// Family is the assigned ProviderProfile.Family.
		Family string `json:"family"`
		// OwnedPaths lists the repository or workspace path globs for this role.
		OwnedPaths []string `json:"owned_paths"`
		// AccessMode describes the workspace boundary ("read_only_after_cp2", "read_write_worktree", "isolated_eval_workspace", or "read_only_review").
		AccessMode string `json:"access_mode"`
	}

	// PlanResult holds the proposed swarm role roster and model-diversity facts
	// surfaced at Checkpoint 2 (CP2).
	PlanResult struct {
		// Status reports whether a complete roster was configured ("configured",
		// "unconfigured" when no authenticated profiles exist, or "missing_capability").
		Status string `json:"status"`
		// Assignments lists the deterministic role assignments when Status is "configured".
		Assignments []Assignment `json:"assignments"`
		// CrossFamilyReview is true if and only if Status is "configured" and the
		// @reviewer role's Family is disjoint from every assigned @builder role's Family.
		CrossFamilyReview bool `json:"cross_family_review"`
		// CrossFamilyEval is true if and only if Status is "configured" and the
		// @eval-author role's Family is disjoint from every assigned @builder role's Family.
		CrossFamilyEval bool `json:"cross_family_eval"`
		// SingleFamily is true when Status is "configured" and all assigned roles
		// use the same model Family.
		SingleFamily bool `json:"single_family"`
		// MissingRoles lists required roles that lacked an authenticated capable
		// profile when Status is "unconfigured" or "missing_capability".
		MissingRoles []RoleKind `json:"missing_roles,omitempty"`
		// Summary is a plain-English description of the proposed roster and
		// cross-family or single-family status.
		Summary string `json:"summary"`
	}

	// ChorusSender is the narrow interface required to publish a pinned
	// Collaboration Manifest to a Chorus channel.
	ChorusSender interface {
		// Send posts text to targetChannel (prefixed with '#') as agentHandle in
		// swarmNamespace, pinning the message as the channel manifest when pin is true.
		Send(ctx context.Context, swarmNamespace, agentHandle, targetChannel, text string, pin bool) error
	}
)

const (
	// RoleArchitect owns the Goa/Goa-AI/Model DSL contract at CP1 and CP2.
	RoleArchitect RoleKind = "architect"
	// RoleBuilder implements Go service and agent code for an assigned scope.
	RoleBuilder RoleKind = "builder"
	// RoleEvalAuthor wires scenario hooks and simulators in the isolated evaluator workspace.
	RoleEvalAuthor RoleKind = "eval_author"
	// RoleReviewer performs a zero-sunk-cost read-only adversarial review before CP4.
	RoleReviewer RoleKind = "reviewer"

	// StatusConfigured indicates all required roles have authenticated capable profiles assigned.
	StatusConfigured = "configured"
	// StatusUnconfigured indicates no authenticated provider profiles are configured.
	StatusUnconfigured = "unconfigured"
	// StatusMissingCapability indicates at least one required role has no authenticated capable profile.
	StatusMissingCapability = "missing_capability"

	// AccessReadOnlyAfterCP2 indicates @architect's design files are frozen during the build phase.
	AccessReadOnlyAfterCP2 = "read_only_after_cp2"
	// AccessReadWriteWorktree indicates an isolated per-builder Git worktree.
	AccessReadWriteWorktree = "read_write_worktree"
	// AccessIsolatedEvalWorkspace indicates the evaluator workspace inaccessible to builders.
	AccessIsolatedEvalWorkspace = "isolated_eval_workspace"
	// AccessReadOnlyReview indicates a fresh read-only review context.
	AccessReadOnlyReview = "read_only_review"

	// maxChorusHandleLen is the maximum length of a Chorus agent handle (`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,31}$`).
	maxChorusHandleLen = 32
	// maxChorusTextLen is the maximum message length accepted by the Chorus send contract.
	maxChorusTextLen = 4000
)

var (
	// requiredRoles is the canonical ordered list of roles every Scantle build requires.
	requiredRoles = []RoleKind{
		RoleArchitect,
		RoleBuilder,
		RoleEvalAuthor,
		RoleReviewer,
	}

	// chorusHandlePattern matches valid Chorus agent handles.
	chorusHandlePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,31}$`)
	// chorusChannelPattern matches valid Chorus channel names.
	chorusChannelPattern = regexp.MustCompile(`^#[a-zA-Z0-9][a-zA-Z0-9_-]{0,31}$`)
)

// Plan computes a deterministic proposed swarm role roster and model routing
// plan from server-configured provider profiles and explicit builder scopes.
//
// When no authenticated profiles are configured or a required role capability
// is missing, Plan returns a non-error PlanResult with Status "unconfigured" or
// "missing_capability" and MissingRoles populated so CP2 architecture previews
// still succeed before coding CLIs are configured, while RequireReady blocks
// actual build job launch. Plan returns an error only when the supplied
// configuration itself is malformed (such as duplicate profile IDs or empty
// required profile metadata).
func Plan(req Request) (PlanResult, error) {
	profiles, err := validateAndSortProfiles(req.Profiles)
	if err != nil {
		return PlanResult{}, err
	}
	builders, err := normalizeBuilders(req.Builders)
	if err != nil {
		return PlanResult{}, err
	}
	archPaths := normalizePaths(req.ArchitectPaths, []string{"design/**"})
	evalPaths := normalizePaths(req.EvalPaths, []string{"*-eval/**"})

	var eligible []ProviderProfile
	for _, p := range profiles {
		if p.Authenticated {
			eligible = append(eligible, p)
		}
	}
	if len(eligible) == 0 {
		return PlanResult{
			Status:       StatusUnconfigured,
			MissingRoles: slices.Clone(requiredRoles),
			Summary:      "Proposed swarm: unconfigured (no authenticated coding-agent profiles available; configure at least one provider before launching build jobs).",
		}, nil
	}

	var missing []RoleKind
	for _, role := range requiredRoles {
		if len(profilesWithCapability(eligible, role)) == 0 {
			missing = append(missing, role)
		}
	}
	if len(missing) > 0 {
		names := make([]string, len(missing))
		for i, r := range missing {
			names[i] = string(r)
		}
		return PlanResult{
			Status:       StatusMissingCapability,
			MissingRoles: missing,
			Summary: fmt.Sprintf(
				"Proposed swarm: missing_capability (no authenticated profile supports required role(s): %s).",
				strings.Join(names, ", "),
			),
		}, nil
	}

	builderProfiles, err := selectBuilderProfiles(eligible, builders)
	if err != nil {
		return PlanResult{}, err
	}
	builderFamilies := make(map[string]bool, len(builderProfiles))
	for _, bp := range builderProfiles {
		builderFamilies[bp.Family] = true
	}

	reviewerProfile := selectDisjointOrFirst(eligible, RoleReviewer, builderFamilies)
	evalProfile := selectDisjointOrFirst(eligible, RoleEvalAuthor, builderFamilies, reviewerProfile.Family)
	archProfile := selectDisjointOrFirst(eligible, RoleArchitect, builderFamilies, reviewerProfile.Family)

	builderHandles := allocateBuilderHandles(builders)
	assignments := make([]Assignment, 0, len(builders)+3)
	assignments = append(assignments, Assignment{
		Handle:     "architect",
		Role:       RoleArchitect,
		ScopeName:  "design",
		ProfileID:  archProfile.ID,
		Provider:   archProfile.Provider,
		Model:      archProfile.Model,
		Family:     archProfile.Family,
		OwnedPaths: archPaths,
		AccessMode: AccessReadOnlyAfterCP2,
	})
	for i, b := range builders {
		bp := builderProfiles[i]
		assignments = append(assignments, Assignment{
			Handle:     builderHandles[i],
			Role:       RoleBuilder,
			ScopeName:  b.Name,
			ProfileID:  bp.ID,
			Provider:   bp.Provider,
			Model:      bp.Model,
			Family:     bp.Family,
			OwnedPaths: b.Paths,
			AccessMode: AccessReadWriteWorktree,
		})
	}
	assignments = append(assignments,
		Assignment{
			Handle:     "eval-author",
			Role:       RoleEvalAuthor,
			ScopeName:  "eval",
			ProfileID:  evalProfile.ID,
			Provider:   evalProfile.Provider,
			Model:      evalProfile.Model,
			Family:     evalProfile.Family,
			OwnedPaths: evalPaths,
			AccessMode: AccessIsolatedEvalWorkspace,
		},
		Assignment{
			Handle:     "reviewer",
			Role:       RoleReviewer,
			ScopeName:  "review",
			ProfileID:  reviewerProfile.ID,
			Provider:   reviewerProfile.Provider,
			Model:      reviewerProfile.Model,
			Family:     reviewerProfile.Family,
			OwnedPaths: []string{"*"},
			AccessMode: AccessReadOnlyReview,
		},
	)

	crossReview := !builderFamilies[reviewerProfile.Family]
	crossEval := !builderFamilies[evalProfile.Family]

	allFamilies := make(map[string]bool, len(assignments))
	for _, a := range assignments {
		allFamilies[a.Family] = true
	}
	singleFamily := len(allFamilies) == 1

	summary := formatPlanSummary(assignments, crossReview, crossEval, singleFamily)
	return PlanResult{
		Status:            StatusConfigured,
		Assignments:       assignments,
		CrossFamilyReview: crossReview,
		CrossFamilyEval:   crossEval,
		SingleFamily:      singleFamily,
		Summary:           summary,
	}, nil
}

// RequireReady verifies that plan has Status "configured" and a complete set of
// assignments before the orchestrator launches any coding-agent job.
func RequireReady(plan PlanResult) error {
	if plan.Status != StatusConfigured {
		return fmt.Errorf("swarm plan is not ready for launch (status=%q): %s", plan.Status, plan.Summary)
	}
	if len(plan.Assignments) < 4 {
		return fmt.Errorf("swarm plan requires at least 4 role assignments, got %d", len(plan.Assignments))
	}
	return nil
}

// FormatManifest renders the deterministic Chorus Collaboration Manifest text
// for a configured PlanResult. Formatting is pure and does not claim that the
// manifest has been published to Chorus.
func FormatManifest(changeID, designDigest string, plan PlanResult) (string, error) {
	if err := RequireReady(plan); err != nil {
		return "", err
	}
	if strings.TrimSpace(changeID) == "" {
		return "", errors.New("change_id is required to format swarm manifest")
	}
	if strings.TrimSpace(designDigest) == "" {
		return "", errors.New("design_digest is required to format swarm manifest")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[SCANTLE SWARM MANIFEST | change=%s | design=%s]\n", strings.TrimSpace(changeID), strings.TrimSpace(designDigest))
	fmt.Fprintf(
		&b,
		"Status: proposed/configured (cross_family_review=%t, cross_family_eval=%t, single_family=%t)\n",
		plan.CrossFamilyReview,
		plan.CrossFamilyEval,
		plan.SingleFamily,
	)
	b.WriteString("Roles & Workspace Boundaries:\n")
	for _, a := range plan.Assignments {
		fmt.Fprintf(
			&b,
			"- @%s (%s | profile=%s | model=%s | family=%s | mode=%s): %s\n",
			a.Handle,
			a.Role,
			a.ProfileID,
			a.Model,
			a.Family,
			a.AccessMode,
			strings.Join(a.OwnedPaths, ", "),
		)
	}
	text := b.String()
	if len(text) > maxChorusTextLen {
		return "", fmt.Errorf("formatted Chorus manifest length %d exceeds Chorus limit of %d bytes", len(text), maxChorusTextLen)
	}
	return text, nil
}

// PublishManifest formats and publishes the pinned Collaboration Manifest to a
// Chorus channel via the provided ChorusSender, returning the published text
// only after the Chorus Send call succeeds.
func PublishManifest(
	ctx context.Context,
	sender ChorusSender,
	swarmNamespace, channel, senderHandle, changeID, designDigest string,
	plan PlanResult,
) (string, error) {
	if sender == nil {
		return "", errors.New("chorus sender is required")
	}
	if !chorusHandlePattern.MatchString(senderHandle) {
		return "", fmt.Errorf("invalid Chorus sender handle %q", senderHandle)
	}
	if !chorusChannelPattern.MatchString(channel) {
		return "", fmt.Errorf("invalid Chorus channel %q (must start with '#')", channel)
	}
	if strings.TrimSpace(swarmNamespace) == "" {
		swarmNamespace = "default"
	}
	manifest, err := FormatManifest(changeID, designDigest, plan)
	if err != nil {
		return "", err
	}
	if err := sender.Send(ctx, swarmNamespace, senderHandle, channel, manifest, true); err != nil {
		return "", fmt.Errorf("publish pinned Chorus manifest to %s: %w", channel, err)
	}
	return manifest, nil
}

// validateAndSortProfiles validates all configured ProviderProfile entries,
// rejects duplicate profile IDs or empty metadata, and returns a canonically
// sorted copy so Plan is invariant to input slice order.
func validateAndSortProfiles(in []ProviderProfile) ([]ProviderProfile, error) {
	if len(in) == 0 {
		return nil, nil
	}
	seenIDs := make(map[string]bool, len(in))
	out := make([]ProviderProfile, 0, len(in))
	for i, p := range in {
		id := strings.TrimSpace(p.ID)
		provider := strings.TrimSpace(p.Provider)
		modelName := strings.TrimSpace(p.Model)
		family := strings.ToLower(strings.TrimSpace(p.Family))
		if id == "" || provider == "" || modelName == "" || family == "" {
			return nil, fmt.Errorf("profiles[%d] requires non-empty id, provider, model, and family", i)
		}
		if seenIDs[id] {
			return nil, fmt.Errorf("duplicate provider profile id %q", id)
		}
		seenIDs[id] = true

		caps := make([]RoleKind, 0, len(p.Capabilities))
		seenCaps := make(map[RoleKind]bool, len(p.Capabilities))
		for _, c := range p.Capabilities {
			switch c {
			case RoleArchitect, RoleBuilder, RoleEvalAuthor, RoleReviewer:
				if !seenCaps[c] {
					seenCaps[c] = true
					caps = append(caps, c)
				}
			default:
				return nil, fmt.Errorf("profile %q declares unsupported capability %q", id, c)
			}
		}
		slices.Sort(caps)
		out = append(out, ProviderProfile{
			ID:            id,
			Provider:      provider,
			Model:         modelName,
			Family:        family,
			Authenticated: p.Authenticated,
			Capabilities:  caps,
		})
	}
	slices.SortFunc(out, func(a, b ProviderProfile) int {
		if a.Family != b.Family {
			return strings.Compare(a.Family, b.Family)
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

// normalizeBuilders validates and canonically sorts BuilderScope entries so
// Plan is invariant to input slice order.
func normalizeBuilders(in []BuilderScope) ([]BuilderScope, error) {
	if len(in) == 0 {
		return []BuilderScope{
			{
				Name:  "service",
				Paths: []string{"cmd/**", "internal/**"},
			},
		}, nil
	}
	seenNames := make(map[string]bool, len(in))
	out := make([]BuilderScope, 0, len(in))
	for i, b := range in {
		name := strings.TrimSpace(b.Name)
		if name == "" {
			return nil, fmt.Errorf("builders[%d] requires non-empty name", i)
		}
		if seenNames[name] {
			return nil, fmt.Errorf("duplicate builder scope name %q", name)
		}
		seenNames[name] = true
		paths := normalizePaths(b.Paths, nil)
		if len(paths) == 0 {
			return nil, fmt.Errorf("builder scope %q requires at least one owned path", name)
		}
		out = append(out, BuilderScope{
			Name:               name,
			Paths:              paths,
			PreferredProfileID: strings.TrimSpace(b.PreferredProfileID),
		})
	}
	slices.SortFunc(out, func(a, b BuilderScope) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out, nil
}

// normalizePaths deduplicates and sorts path globs, falling back to defaults when empty.
func normalizePaths(in, defaults []string) []string {
	seen := make(map[string]bool, len(in))
	var out []string
	for _, p := range in {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" && !seen[trimmed] {
			seen[trimmed] = true
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 && len(defaults) > 0 {
		for _, p := range defaults {
			trimmed := strings.TrimSpace(p)
			if trimmed != "" && !seen[trimmed] {
				seen[trimmed] = true
				out = append(out, trimmed)
			}
		}
	}
	slices.Sort(out)
	return out
}

// profilesWithCapability filters profiles to those advertising role.
func profilesWithCapability(profiles []ProviderProfile, role RoleKind) []ProviderProfile {
	var out []ProviderProfile
	for _, p := range profiles {
		if slices.Contains(p.Capabilities, role) {
			out = append(out, p)
		}
	}
	return out
}

// selectBuilderProfiles chooses an authenticated builder-capable profile for
// each BuilderScope. When no explicit PreferredProfileID overrides it, all
// builders are kept in a single builder family that leaves a distinct family
// available for @reviewer (and @eval-author) whenever possible.
func selectBuilderProfiles(eligible []ProviderProfile, builders []BuilderScope) ([]ProviderProfile, error) {
	builderCapable := profilesWithCapability(eligible, RoleBuilder)
	reviewerCapable := profilesWithCapability(eligible, RoleReviewer)
	evalCapable := profilesWithCapability(eligible, RoleEvalAuthor)

	// Pick the best default builder family: prefer a family B where there exists
	// a reviewer-capable profile with Family != B (and secondarily an
	// eval-capable profile with Family != B).
	var chosenDefault ProviderProfile
	bestScore := -1
	for _, cand := range builderCapable {
		score := 0
		if hasDisjointFamily(reviewerCapable, cand.Family) {
			score += 2
		}
		if hasDisjointFamily(evalCapable, cand.Family) {
			score++
		}
		if score > bestScore {
			bestScore = score
			chosenDefault = cand
		}
	}

	// Collect all profiles in the chosen default builder family so multiple
	// builders can round-robin within that same family without polluting other
	// families needed for cross-family review.
	var familyPool []ProviderProfile
	for _, cand := range builderCapable {
		if cand.Family == chosenDefault.Family {
			familyPool = append(familyPool, cand)
		}
	}

	byID := make(map[string]ProviderProfile, len(builderCapable))
	for _, cand := range builderCapable {
		byID[cand.ID] = cand
	}

	out := make([]ProviderProfile, len(builders))
	for i, b := range builders {
		if b.PreferredProfileID != "" {
			prof, ok := byID[b.PreferredProfileID]
			if !ok {
				return nil, fmt.Errorf("builder %q preferred_profile_id %q is not an authenticated builder-capable profile", b.Name, b.PreferredProfileID)
			}
			out[i] = prof
			continue
		}
		out[i] = familyPool[i%len(familyPool)]
	}
	return out, nil
}

// hasDisjointFamily reports whether any profile in candidates has a Family != family.
func hasDisjointFamily(candidates []ProviderProfile, family string) bool {
	for _, c := range candidates {
		if c.Family != family {
			return true
		}
	}
	return false
}

// selectDisjointOrFirst chooses a profile capable of role whose Family is not
// in avoidFamilies (and, when possible, differs from secondaryAvoidFamily),
// falling back deterministically to the first capable profile.
func selectDisjointOrFirst(
	eligible []ProviderProfile,
	role RoleKind,
	avoidFamilies map[string]bool,
	secondaryAvoidFamily ...string,
) ProviderProfile {
	candidates := profilesWithCapability(eligible, role)
	var secondary string
	if len(secondaryAvoidFamily) > 0 {
		secondary = secondaryAvoidFamily[0]
	}
	for _, c := range candidates {
		if !avoidFamilies[c.Family] && (secondary == "" || c.Family != secondary) {
			return c
		}
	}
	for _, c := range candidates {
		if !avoidFamilies[c.Family] {
			return c
		}
	}
	return candidates[0]
}

// allocateBuilderHandles generates deterministic, collision-free Chorus agent
// handles (`<= 32` characters, matching `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,31}$`)
// for each BuilderScope.
func allocateBuilderHandles(builders []BuilderScope) []string {
	reserved := map[string]bool{
		"architect":   true,
		"eval-author": true,
		"reviewer":    true,
	}
	handles := make([]string, len(builders))
	for i, b := range builders {
		slug := sanitizeSlug(b.Name)
		if slug == "" {
			slug = "service"
		}
		base := "builder-" + slug
		if len(base) > maxChorusHandleLen {
			base = strings.TrimRight(base[:maxChorusHandleLen], "-_")
		}
		candidate := base
		for suffix := 2; reserved[candidate]; suffix++ {
			sfx := fmt.Sprintf("-%d", suffix)
			maxPrefix := maxChorusHandleLen - len(sfx)
			prefix := base
			if len(prefix) > maxPrefix {
				prefix = strings.TrimRight(prefix[:maxPrefix], "-_")
			}
			candidate = prefix + sfx
		}
		reserved[candidate] = true
		handles[i] = candidate
	}
	return handles
}

// sanitizeSlug converts an arbitrary scope name into a lowercase alphanumeric
// hyphen-separated slug suitable for a Chorus handle suffix.
func sanitizeSlug(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		default:
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// formatPlanSummary builds the honest plain-English summary of a configured
// PlanResult, explicitly distinguishing cross-family vs. single-family routing
// and noting that role/model assignment does not replace workspace isolation.
func formatPlanSummary(assignments []Assignment, crossReview, crossEval, singleFamily bool) string {
	parts := make([]string, 0, len(assignments))
	for _, a := range assignments {
		parts = append(parts, fmt.Sprintf("@%s (%s/%s, family=%s)", a.Handle, a.Provider, a.Model, a.Family))
	}
	var posture string
	switch {
	case singleFamily:
		posture = fmt.Sprintf(
			"single-family routing (all roles use family %q; cross_family_review=false; evaluator independence relies on workspace/process isolation)",
			assignments[0].Family,
		)
	case crossReview && crossEval:
		posture = "cross-family review and eval-author routing configured (reviewer and eval-author model families are disjoint from all builders; workspace/process isolation enforces evaluator boundaries)"
	case crossReview:
		posture = "cross-family review configured (reviewer family is disjoint from all builders; eval-author shares a builder family)"
	default:
		posture = "mixed-family routing without disjoint global reviewer (reviewer shares a model family with at least one builder; cross_family_review=false)"
	}
	return fmt.Sprintf("Proposed swarm (%s): %s.", posture, strings.Join(parts, ", "))
}

// Package swarm tests deterministic role planning, cross-family model routing,
// handle collision safety, and Chorus manifest publication.
package swarm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type (
	// recordingChorusSender captures Chorus Send calls for testing PublishManifest.
	recordingChorusSender struct {
		// swarm records the Chorus swarm namespace passed to Send.
		swarm string
		// agent records the sender agent handle passed to Send.
		agent string
		// channel records the target channel passed to Send.
		channel string
		// text records the message payload passed to Send.
		text string
		// pin records whether Send requested a pinned manifest.
		pin bool
		// err is the synthetic error returned by Send when non-nil.
		err error
		// calls counts the number of Send invocations.
		calls int
	}
)

// Send records the Chorus message parameters and returns r.err when configured.
func (r *recordingChorusSender) Send(_ context.Context, swarmNamespace, agentHandle, targetChannel, text string, pin bool) error {
	r.calls++
	if r.err != nil {
		return r.err
	}
	r.swarm = swarmNamespace
	r.agent = agentHandle
	r.channel = targetChannel
	r.text = text
	r.pin = pin
	return nil
}

// TestPlanCrossFamilyRoutingOrderInvarianceAndManifestPublication verifies
// deterministic multi-family role assignment, input-order invariance, and
// pinned Chorus manifest formatting and publication.
func TestPlanCrossFamilyRoutingOrderInvarianceAndManifestPublication(t *testing.T) {
	t.Parallel()

	profiles := []ProviderProfile{
		{
			ID:            "claude-opus",
			Provider:      "anthropic",
			Model:         "claude-opus-4-6",
			Family:        "claude",
			Authenticated: true,
			Capabilities:  allRoles(),
		},
		{
			ID:            "codex-gpt5",
			Provider:      "openai",
			Model:         "gpt-5.2-codex",
			Family:        "gpt",
			Authenticated: true,
			Capabilities:  allRoles(),
		},
		{
			ID:            "gemini-argon",
			Provider:      "google",
			Model:         "gemini-4-argon",
			Family:        "gemini",
			Authenticated: true,
			Capabilities:  allRoles(),
		},
	}
	builders := []BuilderScope{
		{Name: "orders", Paths: []string{"internal/orders/**", "cmd/orders/**"}},
		{Name: "refundflow", Paths: []string{"internal/refundflow/**", "cmd/orders-worker/**"}},
	}

	planForward, err := Plan(Request{
		ChangeID:     "chg-101",
		DesignDigest: "sha256:abc123",
		Profiles:     profiles,
		Builders:     builders,
	})
	require.NoError(t, err)
	require.NoError(t, RequireReady(planForward))
	assert.Equal(t, StatusConfigured, planForward.Status)
	assert.True(t, planForward.CrossFamilyReview)
	assert.True(t, planForward.CrossFamilyEval)
	assert.False(t, planForward.SingleFamily)
	require.Len(t, planForward.Assignments, 5)

	// Reversing input slices must produce the exact same PlanResult and Manifest.
	reversedProfiles := slicesCloneReverse(profiles)
	reversedBuilders := slicesCloneReverse(builders)
	planReverse, err := Plan(Request{
		ChangeID:     "chg-101",
		DesignDigest: "sha256:abc123",
		Profiles:     reversedProfiles,
		Builders:     reversedBuilders,
	})
	require.NoError(t, err)
	assert.Equal(t, planForward, planReverse)

	manifest1, err := FormatManifest("chg-101", "sha256:abc123", planForward)
	require.NoError(t, err)
	manifest2, err := FormatManifest("chg-101", "sha256:abc123", planReverse)
	require.NoError(t, err)
	assert.Equal(t, manifest1, manifest2)
	assert.Contains(t, manifest1, "Status: proposed/configured")
	assert.Contains(t, manifest1, "@builder-orders")
	assert.Contains(t, manifest1, "@builder-refundflow")

	// Verify PublishManifest calls ChorusSender with pin=true and propagates sender errors.
	sender := &recordingChorusSender{}
	published, err := PublishManifest(context.Background(), sender, "run-101", "#build", "architect", "chg-101", "sha256:abc123", planForward)
	require.NoError(t, err)
	assert.Equal(t, manifest1, published)
	assert.Equal(t, 1, sender.calls)
	assert.Equal(t, "run-101", sender.swarm)
	assert.Equal(t, "architect", sender.agent)
	assert.Equal(t, "#build", sender.channel)
	assert.True(t, sender.pin)

	failingSender := &recordingChorusSender{err: errors.New("chorus unavailable")}
	_, err = PublishManifest(context.Background(), failingSender, "run-101", "#build", "architect", "chg-101", "sha256:abc123", planForward)
	require.ErrorContains(t, err, "chorus unavailable")
}

// TestPlanCounterexamplesSingleFamilyUnauthenticatedAndMixedBuilders verifies
// single-family routing, unauthenticated profile filtering, mixed-family
// builder counterexamples, missing capability reporting, unconfigured fallback,
// duplicate profile ID rejection, and Chorus handle length/collision bounds.
func TestPlanCounterexamplesSingleFamilyUnauthenticatedAndMixedBuilders(t *testing.T) {
	t.Parallel()

	// Case 1: 2 installed CLIs, but only 1 is authenticated.
	t.Run("two_installed_only_one_authenticated", func(t *testing.T) {
		t.Parallel()
		plan, err := Plan(Request{
			ChangeID:     "chg-1",
			DesignDigest: "sha256:1",
			Profiles: []ProviderProfile{
				{
					ID:            "claude-opus",
					Provider:      "anthropic",
					Model:         "claude-opus-4-6",
					Family:        "claude",
					Authenticated: true,
					Capabilities:  allRoles(),
				},
				{
					ID:            "codex-unauth",
					Provider:      "openai",
					Model:         "gpt-5.2-codex",
					Family:        "gpt",
					Authenticated: false,
					Capabilities:  allRoles(),
				},
			},
		})
		require.NoError(t, err)
		assert.Equal(t, StatusConfigured, plan.Status)
		assert.True(t, plan.SingleFamily)
		assert.False(t, plan.CrossFamilyReview)
		assert.False(t, plan.CrossFamilyEval)
		assert.Contains(t, plan.Summary, "single-family routing")
		for _, a := range plan.Assignments {
			assert.Equal(t, "claude", a.Family)
		}
	})

	// Case 2: 2 authenticated profiles from the SAME model family (must never be labeled cross-family).
	t.Run("two_profiles_same_family_never_labeled_cross_family", func(t *testing.T) {
		t.Parallel()
		plan, err := Plan(Request{
			ChangeID:     "chg-2",
			DesignDigest: "sha256:2",
			Profiles: []ProviderProfile{
				{
					ID:            "claude-opus",
					Provider:      "anthropic",
					Model:         "claude-opus-4-6",
					Family:        "claude",
					Authenticated: true,
					Capabilities:  allRoles(),
				},
				{
					ID:            "claude-sonnet",
					Provider:      "anthropic",
					Model:         "claude-sonnet-4-6",
					Family:        "claude",
					Authenticated: true,
					Capabilities:  allRoles(),
				},
			},
		})
		require.NoError(t, err)
		assert.Equal(t, StatusConfigured, plan.Status)
		assert.True(t, plan.SingleFamily)
		assert.False(t, plan.CrossFamilyReview)
		assert.False(t, plan.CrossFamilyEval)
		assert.Contains(t, plan.Summary, "single-family routing")
	})

	// Case 3: Mixed-family builders forced across the only 2 families with 1 global reviewer.
	// Because reviewer shares a family with one of the builders, CrossFamilyReview must be false.
	t.Run("mixed_family_builders_with_two_total_families_reports_false_cross_review", func(t *testing.T) {
		t.Parallel()
		plan, err := Plan(Request{
			ChangeID:     "chg-3",
			DesignDigest: "sha256:3",
			Profiles: []ProviderProfile{
				{
					ID:            "claude-opus",
					Provider:      "anthropic",
					Model:         "claude-opus-4-6",
					Family:        "claude",
					Authenticated: true,
					Capabilities:  allRoles(),
				},
				{
					ID:            "codex-gpt5",
					Provider:      "openai",
					Model:         "gpt-5.2-codex",
					Family:        "gpt",
					Authenticated: true,
					Capabilities:  allRoles(),
				},
			},
			Builders: []BuilderScope{
				{Name: "api", Paths: []string{"internal/api/**"}, PreferredProfileID: "claude-opus"},
				{Name: "orders", Paths: []string{"internal/orders/**"}, PreferredProfileID: "codex-gpt5"},
			},
		})
		require.NoError(t, err)
		assert.Equal(t, StatusConfigured, plan.Status)
		assert.False(t, plan.SingleFamily)
		assert.False(t, plan.CrossFamilyReview)
		assert.False(t, plan.CrossFamilyEval)
		assert.Contains(t, plan.Summary, "cross_family_review=false")
	})

	// Case 4: Missing required capability returns explicit missing_capability status and blocks RequireReady.
	t.Run("missing_required_capability", func(t *testing.T) {
		t.Parallel()
		plan, err := Plan(Request{
			ChangeID:     "chg-4",
			DesignDigest: "sha256:4",
			Profiles: []ProviderProfile{
				{
					ID:            "builder-only",
					Provider:      "openai",
					Model:         "gpt-5.2-codex",
					Family:        "gpt",
					Authenticated: true,
					Capabilities:  []RoleKind{RoleArchitect, RoleBuilder, RoleEvalAuthor},
				},
			},
		})
		require.NoError(t, err)
		assert.Equal(t, StatusMissingCapability, plan.Status)
		assert.Equal(t, []RoleKind{RoleReviewer}, plan.MissingRoles)
		assert.Empty(t, plan.Assignments)
		assert.ErrorContains(t, RequireReady(plan), "missing_capability")
	})

	// Case 5: No providers (or zero authenticated providers) returns explicit unconfigured status.
	t.Run("no_providers_returns_unconfigured", func(t *testing.T) {
		t.Parallel()
		plan, err := Plan(Request{
			ChangeID:     "chg-5",
			DesignDigest: "sha256:5",
		})
		require.NoError(t, err)
		assert.Equal(t, StatusUnconfigured, plan.Status)
		assert.Equal(t, requiredRoles, plan.MissingRoles)
		assert.Empty(t, plan.Assignments)
		assert.ErrorContains(t, RequireReady(plan), "unconfigured")
	})

	// Case 6: Duplicate profile IDs and long/colliding builder names.
	t.Run("duplicate_profile_rejected_and_long_colliding_builder_handles_safe", func(t *testing.T) {
		t.Parallel()
		_, err := Plan(Request{
			ChangeID:     "chg-dup",
			DesignDigest: "sha256:dup",
			Profiles: []ProviderProfile{
				{ID: "dup", Provider: "anthropic", Model: "m1", Family: "claude", Authenticated: true, Capabilities: allRoles()},
				{ID: "dup", Provider: "openai", Model: "m2", Family: "gpt", Authenticated: true, Capabilities: allRoles()},
			},
		})
		require.ErrorContains(t, err, "duplicate provider profile id")

		plan, err := Plan(Request{
			ChangeID:     "chg-long",
			DesignDigest: "sha256:long",
			Profiles: []ProviderProfile{
				{ID: "p1", Provider: "anthropic", Model: "m1", Family: "claude", Authenticated: true, Capabilities: allRoles()},
			},
			Builders: []BuilderScope{
				{Name: "Customer Order Refund Processing Subsystem Alpha", Paths: []string{"internal/a/**"}},
				{Name: "Customer Order Refund Processing Subsystem Beta", Paths: []string{"internal/b/**"}},
			},
		})
		require.NoError(t, err)
		seenHandles := make(map[string]bool)
		for _, a := range plan.Assignments {
			assert.LessOrEqual(t, len(a.Handle), maxChorusHandleLen)
			assert.True(t, chorusHandlePattern.MatchString(a.Handle), "invalid Chorus handle %q", a.Handle)
			assert.False(t, seenHandles[a.Handle], "colliding Chorus handle %q", a.Handle)
			seenHandles[a.Handle] = true
			assert.False(t, strings.HasSuffix(a.Handle, "-"))
		}
	})
}

// allRoles returns the complete slice of required Scantle swarm roles.
func allRoles() []RoleKind {
	return []RoleKind{RoleArchitect, RoleBuilder, RoleEvalAuthor, RoleReviewer}
}

// slicesCloneReverse returns a new slice containing the elements of in in reverse order.
func slicesCloneReverse[T any](in []T) []T {
	out := make([]T, len(in))
	for i := range in {
		out[len(in)-1-i] = in[i]
	}
	return out
}

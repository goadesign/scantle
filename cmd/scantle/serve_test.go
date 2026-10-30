package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goadesign/scantle/internal/swarm"
)

func TestNewCP1AnalyzerSelectsModelFromFlags(t *testing.T) {
	env := map[string]string{
		"ANTHROPIC_API_KEY": "test-anthropic-key",
		"OPENAI_API_KEY":    "test-openai-key",
	}
	cases := []struct {
		name         string
		cfg          serveConfig
		env          map[string]string
		wantAnalyzer bool
		wantErr      string
	}{
		{name: "no model flags leaves CP1 unconfigured", cfg: serveConfig{}, env: env},
		{name: "gateway", cfg: serveConfig{gatewayAddr: "127.0.0.1:9090"}, env: env, wantAnalyzer: true},
		{name: "anthropic", cfg: serveConfig{provider: "anthropic", modelName: "claude-test"}, env: env, wantAnalyzer: true},
		{name: "openai", cfg: serveConfig{provider: "openai", modelName: "gpt-test"}, env: env, wantAnalyzer: true},
		{
			name:    "gateway with model name",
			cfg:     serveConfig{gatewayAddr: "127.0.0.1:9090", modelName: "claude-test"},
			env:     env,
			wantErr: "-model-gateway-addr cannot be combined",
		},
		{
			name:    "gateway with provider",
			cfg:     serveConfig{gatewayAddr: "127.0.0.1:9090", provider: "anthropic"},
			env:     env,
			wantErr: "-model-gateway-addr cannot be combined",
		},
		{name: "provider without model", cfg: serveConfig{provider: "anthropic"}, env: env, wantErr: "must be set together"},
		{name: "model without provider", cfg: serveConfig{modelName: "claude-test"}, env: env, wantErr: "must be set together"},
		{
			name:    "unknown provider",
			cfg:     serveConfig{provider: "Anthropic", modelName: "claude-test"},
			env:     env,
			wantErr: `unsupported -model-provider "Anthropic"`,
		},
		{
			name:    "missing api key",
			cfg:     serveConfig{provider: "openai", modelName: "gpt-test"},
			env:     map[string]string{"ANTHROPIC_API_KEY": "test-anthropic-key"},
			wantErr: "-model-provider openai requires OPENAI_API_KEY",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			analyzer, closeModel, err := newCP1Analyzer(tc.cfg, func(k string) string { return tc.env[k] })
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.NotContains(t, err.Error(), "test-anthropic-key")
				assert.NotContains(t, err.Error(), "test-openai-key")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantAnalyzer, analyzer != nil)
			assert.NoError(t, closeModel())
		})
	}
}

func TestLoadSwarmProfilesDecodesStrictly(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []swarm.ProviderProfile
		wantErr string
	}{
		{
			name: "valid profiles",
			content: `[{"id":"claude-a","provider":"anthropic","model":"model-a","family":"claude",` +
				`"authenticated":true,"capabilities":["architect","builder"]}]`,
			want: []swarm.ProviderProfile{{
				ID:            "claude-a",
				Provider:      "anthropic",
				Model:         "model-a",
				Family:        "claude",
				Authenticated: true,
				Capabilities:  []swarm.RoleKind{swarm.RoleArchitect, swarm.RoleBuilder},
			}},
		},
		{name: "unknown field", content: `[{"id":"a","api_key":"x"}]`, wantErr: `unknown field "api_key"`},
		{name: "trailing data", content: `[] []`, wantErr: "trailing data"},
		{name: "empty array", content: `[]`, want: []swarm.ProviderProfile{}},
		{name: "not an array", content: `{"id":"a"}`, wantErr: "decode -swarm-profiles"},
		{name: "null", content: `null`, wantErr: "expected a JSON array, got null"},
		{
			name: "duplicate id",
			content: `[{"id":"a","provider":"openai","model":"m","family":"gpt"},` +
				`{"id":"a","provider":"anthropic","model":"n","family":"claude"}]`,
			wantErr: `duplicate provider profile id "a"`,
		},
		{name: "empty required field", content: `[{"id":"a","provider":"openai","family":"gpt"}]`, wantErr: "requires non-empty"},
		{
			name:    "unknown capability",
			content: `[{"id":"a","provider":"openai","model":"m","family":"gpt","capabilities":["deployer"]}]`,
			wantErr: `unsupported capability "deployer"`,
		},
		{
			name:    "valid profiles without builder capability",
			content: `[{"id":"a","provider":"openai","model":"m","family":"gpt","capabilities":["reviewer"]}]`,
			want: []swarm.ProviderProfile{{
				ID: "a", Provider: "openai", Model: "m", Family: "gpt",
				Capabilities: []swarm.RoleKind{swarm.RoleReviewer},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profiles.json")
			require.NoError(t, os.WriteFile(path, []byte(tc.content), 0o600))
			got, err := loadSwarmProfiles(path)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestServeOptionsWiresAnalyzerAndProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	require.NoError(t, os.WriteFile(path, []byte(`[{"id":"a","provider":"openai","model":"m","family":"gpt"}]`), 0o600))

	opts, closeModel, err := serveOptions(
		serveConfig{provider: "openai", modelName: "gpt-test", swarmProfilesPath: path},
		func(string) string { return "test-key" },
	)
	require.NoError(t, err)
	assert.NotNil(t, opts.Analyzer)
	assert.Equal(t, []swarm.ProviderProfile{{ID: "a", Provider: "openai", Model: "m", Family: "gpt"}}, opts.SwarmProfiles)
	assert.Nil(t, opts.TargetUpdater)
	assert.NoError(t, closeModel())

	opts, closeModel, err = serveOptions(serveConfig{}, func(string) string { return "" })
	require.NoError(t, err)
	assert.Nil(t, opts.Analyzer)
	assert.Nil(t, opts.SwarmProfiles)
	assert.NoError(t, closeModel())

	_, _, err = serveOptions(serveConfig{swarmProfilesPath: filepath.Join(t.TempDir(), "missing.json")}, os.Getenv)
	require.ErrorContains(t, err, "read -swarm-profiles")
}

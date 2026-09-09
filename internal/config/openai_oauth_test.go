package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/oauth"
	openaioauth "github.com/charmbracelet/crush/internal/oauth/openai"
	"github.com/stretchr/testify/require"
)

func TestSetProviderAPIKeyPersistsChatGPTCatalog(t *testing.T) {
	for _, providerID := range []string{openaioauth.ProviderID, openaioauth.LegacyProviderID} {
		t.Run(providerID, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "crush.json")
			require.NoError(t, os.WriteFile(path, []byte(`{}`), 0o600))
			store := &ConfigStore{
				config: &Config{Providers: csync.NewMapFrom(map[string]ProviderConfig{
					providerID: {ID: providerID, Name: "ChatGPT", Type: catwalk.TypeOpenAI},
				})},
				globalDataPath: path,
				listOpenAIModels: func(_ context.Context, token *oauth.Token) ([]catwalk.Model, error) {
					require.Equal(t, "acct_123", token.AccountID)
					return []catwalk.Model{{ID: "gpt-entitled", Name: "Entitled"}}, nil
				},
			}
			token := &oauth.Token{AccessToken: "access", RefreshToken: "refresh", AccountID: "acct_123"}
			require.NoError(t, store.SetProviderAPIKey(ScopeGlobal, providerID, token))

			provider, ok := store.Config().Providers.Get(providerID)
			require.True(t, ok)
			require.Equal(t, openaioauth.CodexBackendURL, provider.BaseURL)
			require.Equal(t, []catwalk.Model{{ID: "gpt-entitled", Name: "Entitled"}}, provider.Models)
			require.True(t, provider.FlatRate)

			var saved struct {
				Providers map[string]ProviderConfig `json:"providers"`
			}
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(data, &saved))
			require.Equal(t, "acct_123", saved.Providers[providerID].OAuthToken.AccountID)
			require.Equal(t, "gpt-entitled", saved.Providers[providerID].Models[0].ID)
		})
	}
}

func TestRefreshOAuthTokenRoutesSubscriptionProviderIDs(t *testing.T) {
	for _, providerID := range []string{openaioauth.ProviderID, openaioauth.LegacyProviderID} {
		t.Run(providerID, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "crush.json")
			require.NoError(t, os.WriteFile(path, []byte(`{}`), 0o600))
			expired := &oauth.Token{
				AccessToken: "old-access", RefreshToken: "old-refresh",
				ExpiresAt: time.Now().Add(-time.Hour).Unix(), AccountID: "acct_123",
			}
			store := &ConfigStore{
				config: &Config{Providers: csync.NewMapFrom(map[string]ProviderConfig{
					providerID: {ID: providerID, OAuthToken: expired},
				})},
				globalDataPath: path,
				exchangeToken: func(_ context.Context, gotProviderID, refreshToken string) (*oauth.Token, error) {
					require.Equal(t, providerID, gotProviderID)
					require.Equal(t, "old-refresh", refreshToken)
					return &oauth.Token{AccessToken: "new-access", ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
				},
				listOpenAIModels: func(_ context.Context, token *oauth.Token) ([]catwalk.Model, error) {
					require.Equal(t, "acct_123", token.AccountID)
					return []catwalk.Model{{ID: "gpt-live"}}, nil
				},
			}

			require.NoError(t, store.RefreshOAuthToken(context.Background(), ScopeGlobal, providerID))
			provider, ok := store.Config().Providers.Get(providerID)
			require.True(t, ok)
			require.Equal(t, openaioauth.CodexBackendURL, provider.BaseURL)
			require.Equal(t, "gpt-live", provider.Models[0].ID)
			require.Equal(t, "old-refresh", provider.OAuthToken.RefreshToken)
		})
	}
}

func TestProjectOpenAISubscriptionProviders(t *testing.T) {
	token := &oauth.Token{AccessToken: "access"}
	for _, tc := range []struct {
		name      string
		providers map[string]ProviderConfig
		catalog   []catwalk.Provider
		wantIDs   []catwalk.InferenceProvider
		wantModel string
	}{
		{
			name: "canonical Codex is appended",
			providers: map[string]ProviderConfig{
				openaioauth.ProviderID: {Name: "ChatGPT (Codex)", BaseURL: openaioauth.CodexBackendURL, OAuthToken: token, Models: []catwalk.Model{{ID: "gpt-live"}}},
			},
			catalog:   []catwalk.Provider{{ID: "anthropic", Models: []catwalk.Model{{ID: "public-model"}}}},
			wantIDs:   []catwalk.InferenceProvider{"anthropic", catwalk.InferenceProvider(openaioauth.ProviderID)},
			wantModel: "gpt-live",
		},
		{
			name: "legacy OpenAI is replaced",
			providers: map[string]ProviderConfig{
				openaioauth.LegacyProviderID: {BaseURL: openaioauth.CodexBackendURL, OAuthToken: token, Models: []catwalk.Model{{ID: "gpt-live"}}},
			},
			catalog:   []catwalk.Provider{{ID: catwalk.InferenceProviderOpenAI, Models: []catwalk.Model{{ID: "public-model"}}}},
			wantIDs:   []catwalk.InferenceProvider{catwalk.InferenceProviderOpenAI},
			wantModel: "gpt-live",
		},
		{
			name: "public OpenAI API key is untouched",
			providers: map[string]ProviderConfig{
				openaioauth.LegacyProviderID: {APIKey: "key", Models: []catwalk.Model{{ID: "configured"}}},
			},
			catalog:   []catwalk.Provider{{ID: catwalk.InferenceProviderOpenAI, Models: []catwalk.Model{{ID: "public-model"}}}},
			wantIDs:   []catwalk.InferenceProvider{catwalk.InferenceProviderOpenAI},
			wantModel: "public-model",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Providers: csync.NewMapFrom(tc.providers)}
			got := ProjectOpenAISubscriptionProviders(cfg, tc.catalog)
			require.Equal(t, tc.wantIDs, providerIDs(got))
			require.Equal(t, tc.wantModel, got[len(got)-1].Models[0].ID)
			require.Equal(t, "public-model", firstModelID(tc.catalog), "input catalog must remain unchanged")
		})
	}
}

func providerIDs(providers []catwalk.Provider) []catwalk.InferenceProvider {
	ids := make([]catwalk.InferenceProvider, len(providers))
	for i := range providers {
		ids[i] = providers[i].ID
	}
	return ids
}

func firstModelID(providers []catwalk.Provider) string {
	if len(providers) == 0 || len(providers[0].Models) == 0 {
		return ""
	}
	return providers[0].Models[0].ID
}

func TestMergeOAuthTokenMetadata(t *testing.T) {
	t.Parallel()
	previous := &oauth.Token{RefreshToken: "refresh", AccountID: "acct", AccountEmail: "u@example.com", AccountPlan: "plus", ChatGPTUserID: "user", AccountFedRAMP: true}
	fresh := &oauth.Token{AccessToken: "new-access"}
	mergeOAuthTokenMetadata(fresh, previous)
	require.Equal(t, "refresh", fresh.RefreshToken)
	require.Equal(t, "acct", fresh.AccountID)
	require.Equal(t, "plus", fresh.AccountPlan)
	require.True(t, fresh.AccountFedRAMP)
}

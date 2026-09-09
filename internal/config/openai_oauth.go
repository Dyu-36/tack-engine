package config

import (
	"context"
	"slices"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/oauth"
	openaioauth "github.com/charmbracelet/crush/internal/oauth/openai"
)

func boolPtr(value bool) *bool { return &value }

// ProjectOpenAISubscriptionProviders overlays the account-scoped catalog from
// config onto the provider list exposed by the backend. Catwalk has no
// canonical "codex" entry, while legacy configs may still store the same
// credential under "openai", so both IDs follow the same credential policy.
// The input slice is never mutated because Providers returns shared cached
// data.
func ProjectOpenAISubscriptionProviders(cfg *Config, providers []catwalk.Provider) []catwalk.Provider {
	projected := slices.Clone(providers)
	if cfg == nil || cfg.Providers == nil {
		return projected
	}

	for _, providerID := range []string{openaioauth.ProviderID, openaioauth.LegacyProviderID} {
		providerConfig, ok := cfg.Providers.Get(providerID)
		if !ok || providerConfig.Disable || len(providerConfig.Models) == 0 ||
			!openaioauth.HasSubscriptionCredential(providerID, providerConfig.OAuthToken) {
			continue
		}

		catalogIndex := slices.IndexFunc(projected, func(provider catwalk.Provider) bool {
			return string(provider.ID) == providerID
		})
		var provider catwalk.Provider
		if catalogIndex >= 0 {
			provider = projected[catalogIndex]
		} else {
			providerConfig.ID = providerID
			provider = providerConfig.ToProvider()
		}
		provider.APIEndpoint = providerConfig.BaseURL
		provider.Models = slices.Clone(providerConfig.Models)
		provider.DefaultLargeModelID = providerConfig.Models[0].ID
		provider.DefaultSmallModelID = providerConfig.Models[0].ID

		if catalogIndex >= 0 {
			projected[catalogIndex] = provider
		} else {
			projected = append(projected, provider)
		}
	}
	return projected
}

func (s *ConfigStore) fetchOpenAIModels(ctx context.Context, token *oauth.Token) ([]catwalk.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if s.listOpenAIModels != nil {
		return s.listOpenAIModels(ctx, token)
	}
	return openaioauth.ListModels(ctx, token)
}

// mergeOAuthTokenMetadata keeps account-routing and display metadata when the
// authorization server omits unchanged fields from a refresh response.
func mergeOAuthTokenMetadata(fresh, previous *oauth.Token) {
	if fresh == nil {
		return
	}
	// A refresh can return a token with no account metadata and no previous
	// entry to copy from. Recovering the claims from the token itself keeps a
	// refresh from downgrading a working credential into one the agent refuses
	// to route.
	defer openaioauth.RecoverAccountMetadata(fresh)
	if previous == nil {
		return
	}
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = previous.RefreshToken
	}
	if fresh.IDToken == "" {
		fresh.IDToken = previous.IDToken
	}
	if fresh.TokenType == "" {
		fresh.TokenType = previous.TokenType
	}
	if fresh.AccountID == "" {
		fresh.AccountID = previous.AccountID
	}
	if fresh.AccountEmail == "" {
		fresh.AccountEmail = previous.AccountEmail
	}
	if fresh.AccountPlan == "" {
		fresh.AccountPlan = previous.AccountPlan
	}
	if fresh.ChatGPTUserID == "" {
		fresh.ChatGPTUserID = previous.ChatGPTUserID
	}
	if !fresh.AccountFedRAMP {
		fresh.AccountFedRAMP = previous.AccountFedRAMP
	}
}

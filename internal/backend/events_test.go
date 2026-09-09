package backend

import (
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/oauth"
	openaioauth "github.com/charmbracelet/crush/internal/oauth/openai"
	"github.com/stretchr/testify/require"
)

func TestGetWorkspaceProvidersProjectsConfiguredCodexCatalog(t *testing.T) {
	b, _ := newTestBackend(t)
	ws, _ := insertTestWorkspace(t, b, "/tmp/codex-catalog")
	ws.Cfg = config.NewTestStore(&config.Config{
		Providers: csync.NewMapFrom(map[string]config.ProviderConfig{
			openaioauth.ProviderID: {
				Name:       "ChatGPT (Codex)",
				BaseURL:    openaioauth.CodexBackendURL,
				OAuthToken: &oauth.Token{AccessToken: "access"},
				Models:     []catwalk.Model{{ID: "gpt-live", Name: "Live"}},
			},
		}),
		Options: &config.Options{DisableDefaultProviders: true},
	})

	value, err := b.GetWorkspaceProviders(ws.ID)
	require.NoError(t, err)
	providers, ok := value.([]catwalk.Provider)
	require.True(t, ok)

	for _, provider := range providers {
		if provider.ID != catwalk.InferenceProvider(openaioauth.ProviderID) {
			continue
		}
		require.Equal(t, openaioauth.CodexBackendURL, provider.APIEndpoint)
		require.Equal(t, "gpt-live", provider.DefaultLargeModelID)
		require.Equal(t, []catwalk.Model{{ID: "gpt-live", Name: "Live"}}, provider.Models)
		return
	}
	t.Fatal("configured Codex provider is missing from the /providers projection")
}

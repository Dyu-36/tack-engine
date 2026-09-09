package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/stretchr/testify/require"
)

func TestClientListModelsUsesSubscriptionProtocol(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/models", r.URL.Path)
		require.Equal(t, "test-version", r.URL.Query().Get("client_version"))
		require.Equal(t, "Bearer access", r.Header.Get("Authorization"))
		require.Equal(t, "acct_123", r.Header.Get("ChatGPT-Account-Id"))
		require.Equal(t, "gotack-test", r.Header.Get("originator"))
		require.Equal(t, "test-version", r.Header.Get("version"))
		require.Equal(t, "gotack-test/test-version", r.Header.Get("User-Agent"))
		require.Equal(t, "true", r.Header.Get("X-OpenAI-Fedramp"))
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{
			map[string]any{"slug": "hidden", "display_name": "Hidden", "visibility": "hide", "priority": 0},
			map[string]any{"slug": "gpt-second", "display_name": "Second", "visibility": "list", "priority": 2},
			map[string]any{
				"slug": "gpt-first", "display_name": "First", "visibility": "list", "priority": 1,
				"context_window": 200000, "default_reasoning_level": "medium",
				"supported_reasoning_levels": []any{map[string]any{"effort": "low"}, map[string]any{"effort": "medium"}},
				"input_modalities":           []string{"text", "image"},
			},
		}})
	}))
	defer server.Close()

	client := Client{HTTPClient: server.Client(), BaseURL: server.URL, Version: "test-version", Originator: "gotack-test"}
	models, err := client.ListModels(context.Background(), &oauth.Token{AccessToken: "access", AccountID: "acct_123", AccountFedRAMP: true})
	require.NoError(t, err)
	require.Len(t, models, 2)
	require.Equal(t, "gpt-first", models[0].ID)
	require.Equal(t, int64(200000), models[0].ContextWindow)
	require.Equal(t, []string{"low", "medium"}, models[0].ReasoningLevels)
	require.True(t, models[0].SupportsImages)
}

func TestDefaultClientUsesCodexProtocolVersion(t *testing.T) {
	t.Parallel()
	client := DefaultClient()
	require.Equal(t, CodexClientVersion, client.Version)
	require.NotEqual(t, "devel", client.Version)

	headers := Headers("acct_123", false)
	require.Equal(t, CodexClientVersion, headers["version"])
	require.Equal(t, "gotack", headers["originator"])
	require.Equal(t, "gotack/"+CodexClientVersion, headers["User-Agent"])
}

func TestClientRefreshToken(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		require.Equal(t, "refresh_token", r.Form.Get("grant_type"))
		require.Equal(t, "client-test", r.Form.Get("client_id"))
		require.Equal(t, "old-refresh", r.Form.Get("refresh_token"))
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "expires_in": 3600})
	}))
	defer server.Close()

	client := Client{HTTPClient: server.Client(), TokenURL: server.URL, ClientID: "client-test"}
	token, err := client.RefreshToken(context.Background(), "old-refresh")
	require.NoError(t, err)
	require.Equal(t, "new-access", token.AccessToken)
	require.Equal(t, "old-refresh", token.RefreshToken)
	require.Greater(t, token.ExpiresAt, int64(0))
}

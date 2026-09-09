package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	fantasyopenai "charm.land/fantasy/providers/openai"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/oauth"
	openaioauth "github.com/charmbracelet/crush/internal/oauth/openai"
	"github.com/stretchr/testify/require"
)

func TestApplyOpenAIOAuthRouting(t *testing.T) {
	t.Parallel()
	headers := map[string]string{"version": "wrong"}
	baseURL, err := applyOpenAIOAuthRouting(config.ProviderConfig{
		ID:         string(catwalk.InferenceProviderOpenAI),
		OAuthToken: &oauth.Token{AccessToken: "access", AccountID: "acct_123", AccountFedRAMP: true},
	}, "https://api.openai.com/v1", headers)
	require.NoError(t, err)
	require.Equal(t, openaioauth.CodexBackendURL, baseURL)
	require.Equal(t, "acct_123", headers["ChatGPT-Account-Id"])
	require.Equal(t, "gotack", headers["originator"])
	require.Equal(t, openaioauth.CodexClientVersion, headers["version"])
	require.Equal(t, "gotack/"+openaioauth.CodexClientVersion, headers["User-Agent"])
	require.Equal(t, "true", headers["X-OpenAI-Fedramp"])
	require.True(t, omitMaxOutputTokens(config.ProviderConfig{
		ID:         string(catwalk.InferenceProviderOpenAI),
		OAuthToken: &oauth.Token{AccessToken: "access"},
	}))
}

func TestApplyOpenAIOAuthRoutingRequiresAccountID(t *testing.T) {
	t.Parallel()
	_, err := applyOpenAIOAuthRouting(config.ProviderConfig{
		ID:         string(catwalk.InferenceProviderOpenAI),
		OAuthToken: &oauth.Token{AccessToken: "access"},
	}, "", map[string]string{})
	require.ErrorContains(t, err, "account id")
}

func TestApplyOpenAIOAuthRoutingAcceptsCodexProvider(t *testing.T) {
	t.Parallel()
	headers := map[string]string{}
	baseURL, err := applyOpenAIOAuthRouting(config.ProviderConfig{
		ID:         openaioauth.ProviderID,
		OAuthToken: &oauth.Token{AccessToken: "access", AccountID: "acct_123"},
	}, "https://api.openai.com/v1", headers)
	require.NoError(t, err)
	require.Equal(t, openaioauth.CodexBackendURL, baseURL)
	require.Equal(t, "acct_123", headers["ChatGPT-Account-Id"])
}

// An OpenAI provider authenticated with an API key must keep talking to the
// public API: that is the whole point of splitting Codex off.
func TestApplyOpenAIOAuthRoutingLeavesAPIKeyOpenAIAlone(t *testing.T) {
	t.Parallel()
	headers := map[string]string{}
	baseURL, err := applyOpenAIOAuthRouting(config.ProviderConfig{
		ID:     string(catwalk.InferenceProviderOpenAI),
		APIKey: "sk-test",
	}, "https://api.openai.com/v1", headers)
	require.NoError(t, err)
	require.Equal(t, "https://api.openai.com/v1", baseURL)
	require.Empty(t, headers)
	require.False(t, omitMaxOutputTokens(config.ProviderConfig{
		ID:     string(catwalk.InferenceProviderOpenAI),
		APIKey: "sk-test",
	}))
}

func TestMaxOutputTokensForModel(t *testing.T) {
	t.Parallel()

	tokens := maxOutputTokensForModel(Model{}, 128000)
	require.NotNil(t, tokens)
	require.Equal(t, int64(128000), *tokens)
	require.Nil(t, maxOutputTokensForModel(Model{}, 0))
	require.Nil(t, maxOutputTokensForModel(Model{OmitMaxOutputTokens: true}, 128000))
}

func TestMaxOutputTokensResponsesWireContract(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		omit bool
	}{
		{name: "ChatGPT subscription omits the unsupported field", omit: true},
		{name: "public OpenAI API keeps the supported field", omit: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var requestBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/responses", r.URL.Path)
				require.NoError(t, json.NewDecoder(r.Body).Decode(&requestBody))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"resp_test","object":"response","created_at":0,"status":"completed","model":"gpt-5.6-terra","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
			}))
			defer server.Close()

			provider, err := fantasyopenai.New(
				fantasyopenai.WithAPIKey("test-token"),
				fantasyopenai.WithBaseURL(server.URL),
				fantasyopenai.WithUseResponsesAPI(),
			)
			require.NoError(t, err)
			languageModel, err := provider.LanguageModel(t.Context(), "gpt-5.6-terra")
			require.NoError(t, err)

			maxOutputTokens := maxOutputTokensForModel(Model{OmitMaxOutputTokens: tc.omit}, 128000)
			_, err = languageModel.Generate(t.Context(), fantasy.Call{
				Prompt:          fantasy.Prompt{fantasy.NewUserMessage("hello")},
				MaxOutputTokens: maxOutputTokens,
			})
			require.NoError(t, err)

			got, present := requestBody["max_output_tokens"]
			if tc.omit {
				require.False(t, present, "subscription request must omit max_output_tokens, got %v", got)
				return
			}
			require.True(t, present)
			require.Equal(t, float64(128000), got)
		})
	}
}

package agent

import (
	"errors"
	"maps"

	"github.com/charmbracelet/crush/internal/config"
	openaioauth "github.com/charmbracelet/crush/internal/oauth/openai"
)

// applyOpenAIOAuthRouting switches an OAuth-authenticated ChatGPT subscription
// provider from the public API endpoint to the Codex backend and adds the
// account-routing headers expected by that backend.
//
// Routing is keyed on the credential, not on the provider id alone. "codex"
// carries the subscription login while "openai" keeps working for configs
// written before the two were split; an "openai" provider holding an API key
// has no OAuth token and still talks to the public OpenAI API.
func applyOpenAIOAuthRouting(provider config.ProviderConfig, baseURL string, headers map[string]string) (string, error) {
	if !openaioauth.HasSubscriptionCredential(provider.ID, provider.OAuthToken) {
		return baseURL, nil
	}
	if provider.OAuthToken.AccessToken == "" {
		return "", errors.New("OpenAI OAuth credential is missing an access token")
	}
	// Credentials stored before subscription routing existed, and refresh
	// responses that omit unchanged fields, arrive without an account id. The
	// tokens themselves still carry the claim, so recover it here: refusing
	// would strand the user in an app that cannot open a workspace, which is the
	// only place the sign-in this error asks for actually lives.
	openaioauth.RecoverAccountMetadata(provider.OAuthToken)
	if provider.OAuthToken.AccountID == "" {
		return "", errors.New("OpenAI OAuth credential is missing the ChatGPT account id; sign in again")
	}
	maps.Copy(headers, openaioauth.Headers(provider.OAuthToken.AccountID, provider.OAuthToken.AccountFedRAMP))
	return openaioauth.CodexBackendURL, nil
}

// omitMaxOutputTokens reports whether the request is routed to ChatGPT's
// subscription-backed Codex endpoint. Unlike the public Responses API, that
// endpoint rejects the max_output_tokens request field outright.
func omitMaxOutputTokens(provider config.ProviderConfig) bool {
	return openaioauth.HasSubscriptionCredential(provider.ID, provider.OAuthToken)
}

// maxOutputTokensForModel preserves explicit/default output limits everywhere
// except backends whose request contract rejects the field. Returning nil keeps
// Fantasy from serializing max_output_tokens at all.
func maxOutputTokensForModel(model Model, tokens int64) *int64 {
	if tokens <= 0 || model.OmitMaxOutputTokens {
		return nil
	}
	return &tokens
}

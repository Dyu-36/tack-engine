package openai

import "github.com/charmbracelet/crush/internal/oauth"

// ProviderID is the Crush provider id reserved for ChatGPT subscription
// authentication. Keeping it separate from the public "openai" provider lets a
// user hold an OpenAI API key and a ChatGPT subscription at the same time
// without either credential overwriting the other's endpoint or model catalog.
const ProviderID = "codex"

// LegacyProviderID is where ChatGPT OAuth credentials lived before the split.
// Configs written by older builds keep working: routing is decided by the
// credential, so an upgrade never invalidates a login that already works.
const LegacyProviderID = "openai"

// IsSubscriptionProvider reports whether a provider id may hold a ChatGPT
// subscription credential.
//
// Callers must still require an OAuth token before applying subscription
// routing. An "openai" provider authenticated with an API key has no OAuth
// token and must keep talking to the public OpenAI API.
func IsSubscriptionProvider(providerID string) bool {
	return providerID == ProviderID || providerID == LegacyProviderID
}

// HasSubscriptionCredential is the single routing policy for ChatGPT
// subscription behavior. The legacy "openai" id only takes this path when it
// actually contains an OAuth credential, so API-key configurations continue to
// use the public OpenAI endpoint.
func HasSubscriptionCredential(providerID string, token *oauth.Token) bool {
	return token != nil && IsSubscriptionProvider(providerID)
}

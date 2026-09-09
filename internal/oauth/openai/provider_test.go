package openai

import (
	"testing"

	"github.com/charmbracelet/crush/internal/oauth"
)

func TestIsSubscriptionProvider(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		providerID string
		want       bool
	}{
		{ProviderID, true},
		{LegacyProviderID, true},
		{"anthropic", false},
		{"", false},
	} {
		if got := IsSubscriptionProvider(tc.providerID); got != tc.want {
			t.Fatalf("IsSubscriptionProvider(%q) = %v, want %v", tc.providerID, got, tc.want)
		}
	}
}

func TestHasSubscriptionCredential(t *testing.T) {
	t.Parallel()
	token := &oauth.Token{AccessToken: "access"}
	for _, tc := range []struct {
		name       string
		providerID string
		token      *oauth.Token
		want       bool
	}{
		{"codex OAuth", ProviderID, token, true},
		{"legacy OpenAI OAuth", LegacyProviderID, token, true},
		{"public OpenAI API key", LegacyProviderID, nil, false},
		{"other OAuth provider", "anthropic", token, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasSubscriptionCredential(tc.providerID, tc.token); got != tc.want {
				t.Fatalf("HasSubscriptionCredential(%q, %v) = %v, want %v", tc.providerID, tc.token != nil, got, tc.want)
			}
		})
	}
}

func TestSubscriptionProviderIDIsNotThePublicOpenAIProvider(t *testing.T) {
	t.Parallel()
	if ProviderID == LegacyProviderID {
		t.Fatal("the subscription provider must not reuse the public OpenAI provider id")
	}
}

package openai

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/charmbracelet/crush/internal/oauth"
)

func testJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func TestMetadataFromJWTReadsChatGPTClaims(t *testing.T) {
	token := testJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id":         "acct-123",
			"chatgpt_plan_type":          "pro",
			"chatgpt_user_id":            "user-789",
			"chatgpt_account_is_fedramp": true,
		},
		"https://api.openai.com/profile": map[string]any{"email": "user@example.com"},
	})

	got := MetadataFromJWT(token)
	want := AccountMetadata{
		AccountID: "acct-123",
		Email:     "user@example.com",
		Plan:      "pro",
		UserID:    "user-789",
		FedRAMP:   true,
	}
	if got != want {
		t.Fatalf("metadata = %+v, want %+v", got, want)
	}
}

func TestMetadataFromJWTIgnoresMalformedTokens(t *testing.T) {
	cases := map[string]string{
		"empty":           "",
		"opaque":          "sk-live-abc123",
		"bad base64":      "a.!!!.c",
		"payload no json": "a." + base64.RawURLEncoding.EncodeToString([]byte("nope")) + ".c",
	}
	for name, token := range cases {
		if got := MetadataFromJWT(token); got != (AccountMetadata{}) {
			t.Fatalf("%s: metadata = %+v, want zero value", name, got)
		}
	}
}

func TestRecoverAccountMetadataBackfillsFromAccessToken(t *testing.T) {
	// Reproduces the credential that made workspace creation fail: both tokens
	// are present and valid, but every account field was persisted empty.
	token := &oauth.Token{AccessToken: testJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-abc",
			"chatgpt_plan_type":  "plus",
			"chatgpt_user_id":    "user-1",
		},
	})}

	if !RecoverAccountMetadata(token) {
		t.Fatal("expected recovery to report a change")
	}
	if token.AccountID != "acct-abc" {
		t.Fatalf("account id = %q, want acct-abc", token.AccountID)
	}
	if token.AccountPlan != "plus" {
		t.Fatalf("account plan = %q, want plus", token.AccountPlan)
	}
	if token.ChatGPTUserID != "user-1" {
		t.Fatalf("chatgpt user id = %q, want user-1", token.ChatGPTUserID)
	}
}

func TestRecoverAccountMetadataKeepsStoredValues(t *testing.T) {
	token := &oauth.Token{
		AccessToken: testJWT(t, map[string]any{
			"https://api.openai.com/auth": map[string]any{
				"chatgpt_account_id": "acct-from-token",
				"chatgpt_plan_type":  "pro",
				"chatgpt_user_id":    "user-from-token",
			},
		}),
		AccountID:     "acct-stored",
		AccountPlan:   "team",
		ChatGPTUserID: "user-stored",
		AccountEmail:  "stored@example.com",
	}

	if RecoverAccountMetadata(token) {
		t.Fatal("expected no change when the credential is already complete")
	}
	if token.AccountID != "acct-stored" || token.AccountPlan != "team" {
		t.Fatalf("stored metadata was overwritten: %+v", token)
	}
}

func TestRecoverAccountMetadataPrefersIDToken(t *testing.T) {
	token := &oauth.Token{
		IDToken: testJWT(t, map[string]any{
			"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-id-token"},
		}),
		AccessToken: testJWT(t, map[string]any{
			"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-access-token"},
		}),
	}

	if !RecoverAccountMetadata(token) {
		t.Fatal("expected recovery to report a change")
	}
	if token.AccountID != "acct-id-token" {
		t.Fatalf("account id = %q, want acct-id-token", token.AccountID)
	}
}

func TestRecoverAccountMetadataToleratesNilAndOpaqueTokens(t *testing.T) {
	if RecoverAccountMetadata(nil) {
		t.Fatal("nil token must not report a change")
	}
	token := &oauth.Token{AccessToken: "opaque-not-a-jwt"}
	if RecoverAccountMetadata(token) {
		t.Fatal("opaque token must not report a change")
	}
	if token.AccountID != "" {
		t.Fatalf("account id = %q, want empty", token.AccountID)
	}
}

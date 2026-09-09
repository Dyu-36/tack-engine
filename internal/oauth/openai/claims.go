package openai

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/charmbracelet/crush/internal/oauth"
)

// AccountMetadata is the ChatGPT routing data carried by the unencrypted
// payload of an OpenAI-issued JWT. Signature validation stays the
// authorization server's job: these claims only address the right
// subscription account and are never treated as proof of authentication.
type AccountMetadata struct {
	AccountID string
	Email     string
	Plan      string
	UserID    string
	FedRAMP   bool
}

// MetadataFromJWT decodes the payload segment of an OpenAI id token or access
// token; both carry the same namespaced claims. A malformed or foreign token
// yields the zero value instead of an error, because callers treat missing
// metadata as unknown rather than as a failure.
func MetadataFromJWT(token string) AccountMetadata {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return AccountMetadata{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return AccountMetadata{}
	}
	var claims struct {
		Email   string `json:"email"`
		Profile struct {
			Email string `json:"email"`
		} `json:"https://api.openai.com/profile"`
		Auth struct {
			AccountID    string `json:"chatgpt_account_id"`
			PlanType     string `json:"chatgpt_plan_type"`
			UserID       string `json:"chatgpt_user_id"`
			LegacyUserID string `json:"user_id"`
			FedRAMP      bool   `json:"chatgpt_account_is_fedramp"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return AccountMetadata{}
	}
	metadata := AccountMetadata{
		AccountID: claims.Auth.AccountID,
		Email:     claims.Email,
		Plan:      claims.Auth.PlanType,
		UserID:    claims.Auth.UserID,
		FedRAMP:   claims.Auth.FedRAMP,
	}
	if metadata.Email == "" {
		metadata.Email = claims.Profile.Email
	}
	if metadata.UserID == "" {
		metadata.UserID = claims.Auth.LegacyUserID
	}
	return metadata
}

// RecoverAccountMetadata backfills routing metadata a stored credential is
// missing by re-reading the claims of the tokens it already carries. Tokens
// persisted by builds that predate ChatGPT subscription support, and refresh
// responses that omit unchanged fields, keep working instead of forcing an
// interactive sign-in the user may have no way to reach. It reports whether
// the token changed.
func RecoverAccountMetadata(token *oauth.Token) bool {
	if token == nil {
		return false
	}
	changed := false
	for _, jwt := range []string{token.IDToken, token.AccessToken} {
		if jwt == "" {
			continue
		}
		metadata := MetadataFromJWT(jwt)
		if token.AccountID == "" && metadata.AccountID != "" {
			token.AccountID = metadata.AccountID
			changed = true
		}
		if token.AccountEmail == "" && metadata.Email != "" {
			token.AccountEmail = metadata.Email
			changed = true
		}
		if token.AccountPlan == "" && metadata.Plan != "" {
			token.AccountPlan = metadata.Plan
			changed = true
		}
		if token.ChatGPTUserID == "" && metadata.UserID != "" {
			token.ChatGPTUserID = metadata.UserID
			changed = true
		}
		if !token.AccountFedRAMP && metadata.FedRAMP {
			token.AccountFedRAMP = true
			changed = true
		}
	}
	return changed
}

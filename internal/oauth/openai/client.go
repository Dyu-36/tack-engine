package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/oauth"
)

const (
	ClientID          = "app_EMoamEEZ73f0CkXaXp7hrann"
	TokenURL          = "https://auth.openai.com/oauth/token"
	CodexBackendURL   = "https://chatgpt.com/backend-api/codex"
	defaultOriginator = "gotack"

	// CodexClientVersion is the Codex backend protocol compatibility level.
	// Keep it independent from Crush/Gotack's application version: the Codex
	// backend uses this value for model/capability negotiation.
	CodexClientVersion = "0.150.1"
)

// Client talks to the ChatGPT Codex OAuth and subscription endpoints. Fields
// are configurable so protocol tests can use an httptest server.
type Client struct {
	HTTPClient *http.Client
	TokenURL   string
	BaseURL    string
	ClientID   string
	Version    string
	Originator string
}

func DefaultClient() Client {
	return Client{
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
		TokenURL:   TokenURL,
		BaseURL:    CodexBackendURL,
		ClientID:   ClientID,
		Version:    CodexClientVersion,
		Originator: defaultOriginator,
	}
}

// Headers returns the non-secret headers required by ChatGPT's Codex backend.
func Headers(accountID string, fedRAMP bool) map[string]string {
	headers := map[string]string{
		"ChatGPT-Account-Id": accountID,
		"originator":         defaultOriginator,
		"version":            CodexClientVersion,
		"User-Agent":         "gotack/" + CodexClientVersion,
	}
	if fedRAMP {
		headers["X-OpenAI-Fedramp"] = "true"
	}
	return headers
}

func (c Client) normalized() Client {
	d := DefaultClient()
	if c.HTTPClient == nil {
		c.HTTPClient = d.HTTPClient
	}
	if c.TokenURL == "" {
		c.TokenURL = d.TokenURL
	}
	if c.BaseURL == "" {
		c.BaseURL = d.BaseURL
	}
	if c.ClientID == "" {
		c.ClientID = d.ClientID
	}
	if c.Version == "" {
		c.Version = d.Version
	}
	if c.Originator == "" {
		c.Originator = d.Originator
	}
	return c
}

func RefreshToken(ctx context.Context, refreshToken string) (*oauth.Token, error) {
	return DefaultClient().RefreshToken(ctx, refreshToken)
}

func (c Client) RefreshToken(ctx context.Context, refreshToken string) (*oauth.Token, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, errors.New("refresh token is required")
	}
	c = c.normalized()
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {c.ClientID},
		"refresh_token": {refreshToken},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create OpenAI token refresh request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh OpenAI OAuth token: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read OpenAI token refresh response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &oauth.TokenExchangeError{StatusCode: resp.StatusCode, Body: string(body)}
	}
	var token oauth.Token
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, fmt.Errorf("decode OpenAI token refresh response: %w", err)
	}
	if token.AccessToken == "" {
		return nil, errors.New("OpenAI token refresh response missing access_token")
	}
	if token.RefreshToken == "" {
		token.RefreshToken = refreshToken
	}
	token.SetExpiresAt()
	return &token, nil
}

type modelsResponse struct {
	Models []modelInfo `json:"models"`
}

type modelInfo struct {
	Slug                     string `json:"slug"`
	DisplayName              string `json:"display_name"`
	Visibility               string `json:"visibility"`
	Priority                 int    `json:"priority"`
	ContextWindow            int64  `json:"context_window"`
	DefaultReasoningLevel    string `json:"default_reasoning_level"`
	SupportedReasoningLevels []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
	InputModalities []string `json:"input_modalities"`
}

func ListModels(ctx context.Context, token *oauth.Token) ([]catwalk.Model, error) {
	return DefaultClient().ListModels(ctx, token)
}

func (c Client) ListModels(ctx context.Context, token *oauth.Token) ([]catwalk.Model, error) {
	if token == nil || token.AccessToken == "" {
		return nil, errors.New("OpenAI access token is required")
	}
	if token.AccountID == "" {
		return nil, errors.New("ChatGPT account id is required")
	}
	c = c.normalized()
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/models?client_version=" + url.QueryEscape(c.Version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create ChatGPT model request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("ChatGPT-Account-Id", token.AccountID)
	req.Header.Set("originator", c.Originator)
	req.Header.Set("version", c.Version)
	req.Header.Set("User-Agent", c.Originator+"/"+c.Version)
	if token.AccountFedRAMP {
		req.Header.Set("X-OpenAI-Fedramp", "true")
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list ChatGPT models: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read ChatGPT model response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("list ChatGPT models: status %d body %q", resp.StatusCode, string(body))
	}
	var payload modelsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode ChatGPT model response: %w", err)
	}
	slices.SortStableFunc(payload.Models, func(a, b modelInfo) int { return a.Priority - b.Priority })
	models := make([]catwalk.Model, 0, len(payload.Models))
	for _, remote := range payload.Models {
		if remote.Slug == "" || remote.Visibility != "list" {
			continue
		}
		levels := make([]string, 0, len(remote.SupportedReasoningLevels))
		for _, level := range remote.SupportedReasoningLevels {
			if level.Effort != "" {
				levels = append(levels, level.Effort)
			}
		}
		name := remote.DisplayName
		if name == "" {
			name = remote.Slug
		}
		supportsImages := remote.InputModalities == nil || slices.Contains(remote.InputModalities, "image")
		models = append(models, catwalk.Model{
			ID:                     remote.Slug,
			Name:                   name,
			ContextWindow:          remote.ContextWindow,
			DefaultMaxTokens:       32768,
			CanReason:              len(levels) > 0,
			ReasoningLevels:        levels,
			DefaultReasoningEffort: remote.DefaultReasoningLevel,
			SupportsImages:         supportsImages,
		})
	}
	if len(models) == 0 {
		return nil, errors.New("ChatGPT model catalog returned no selectable models")
	}
	return models, nil
}

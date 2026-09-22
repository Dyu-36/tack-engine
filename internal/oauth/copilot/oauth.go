package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/charmbracelet/crush/internal/oauth"
)

const (
	copilotTokenURL = "https://api.github.com/copilot_internal/v2/token"
)

var ErrNotAvailable = errors.New("github copilot not available")

func getCopilotToken(ctx context.Context, githubToken string) (*oauth.Token, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", copilotTokenURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", githubToken))
	for k, v := range Headers() {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusForbidden {
		return nil, ErrNotAvailable
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("copilot token request failed: %s - %s", resp.Status, string(body))
	}

	var result struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	copilotToken := &oauth.Token{
		AccessToken:  result.Token,
		RefreshToken: githubToken,
		ExpiresAt:    result.ExpiresAt,
	}
	copilotToken.SetExpiresIn()

	return copilotToken, nil
}

// RefreshToken refreshes the Copilot token using the GitHub token.
func RefreshToken(ctx context.Context, githubToken string) (*oauth.Token, error) {
	return getCopilotToken(ctx, githubToken)
}

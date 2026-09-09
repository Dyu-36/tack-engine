package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openaicompat"
	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockSessionAgent is a minimal mock for the SessionAgent interface.
type mockSessionAgent struct {
	model         Model
	runFunc       func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error)
	cancelled     []string
	systemPrompts []string
}

func (m *mockSessionAgent) Run(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
	return m.runFunc(ctx, call)
}

func (m *mockSessionAgent) BeginAccepted(sessionID string) *AcceptedRun {
	return &AcceptedRun{sessionID: sessionID}
}

func (m *mockSessionAgent) Model() Model                       { return m.model }
func (m *mockSessionAgent) SetModels(large, small Model)       {}
func (m *mockSessionAgent) SetTools(tools []fantasy.AgentTool) {}
func (m *mockSessionAgent) SetPromptBuild(build prompt.PromptBuild) {
	m.SetSystemPrompt(build.Text)
}

func (m *mockSessionAgent) SetSystemPrompt(systemPrompt string) {
	m.systemPrompts = append(m.systemPrompts, systemPrompt)
}
func (m *mockSessionAgent) Cancel(sessionID string) {
	m.cancelled = append(m.cancelled, sessionID)
}
func (m *mockSessionAgent) CancelAll()                                  {}
func (m *mockSessionAgent) IsSessionBusy(sessionID string) bool         { return false }
func (m *mockSessionAgent) IsBusy() bool                                { return false }
func (m *mockSessionAgent) QueuedPrompts(sessionID string) int          { return 0 }
func (m *mockSessionAgent) QueuedPromptsList(sessionID string) []string { return nil }
func (m *mockSessionAgent) ClearQueue(sessionID string)                 {}
func (m *mockSessionAgent) Summarize(context.Context, string, fantasy.ProviderOptions, func(context.Context, *fantasy.ProviderError) error) error {
	return nil
}
func (m *mockSessionAgent) GenerateTitle(context.Context, string, string) {}

// newTestCoordinator creates a minimal coordinator for unit testing runSubAgent.
func newTestCoordinator(t *testing.T, env fakeEnv, providerID string, providerCfg config.ProviderConfig) *coordinator {
	cfg, err := config.Init(env.workingDir, "", false)
	require.NoError(t, err)
	cfg.Config().Providers.Set(providerID, providerCfg)
	return &coordinator{
		cfg:      cfg,
		sessions: env.sessions,
		messages: env.messages,
	}
}

// newMockAgent creates a mockSessionAgent with the given provider and run function.
func newMockAgent(providerID string, maxTokens int64, runFunc func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error)) *mockSessionAgent {
	return &mockSessionAgent{
		model: Model{
			CatwalkCfg: catwalk.Model{
				DefaultMaxTokens: maxTokens,
			},
			ModelCfg: config.SelectedModel{
				Provider: providerID,
			},
		},
		runFunc: runFunc,
	}
}

// agentResultWithText creates a minimal AgentResult with the given text response.
func agentResultWithText(text string) *fantasy.AgentResult {
	return &fantasy.AgentResult{
		Response: fantasy.Response{
			Content: fantasy.ResponseContent{
				fantasy.TextContent{Text: text},
			},
		},
	}
}

func TestRefreshSkillsUpdatesNextTurnPromptIndex(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	skillsRoot := filepath.Join(workingDir, "learned-skills")
	require.NoError(t, os.MkdirAll(skillsRoot, 0o755))
	cfg, err := config.Init(workingDir, t.TempDir(), false)
	require.NoError(t, err)
	cfg.Config().Options.SkillsPaths = []string{skillsRoot}

	discoveryCfg := skillsDiscoveryConfig(cfg)
	all, active, states := skills.DiscoverFromConfig(discoveryCfg)
	mgr := skills.NewManager(
		all, active, states,
		skills.WithResolvedPaths(discoveryCfg.ResolvePaths()),
		skills.WithWorkingDir(discoveryCfg.WorkingDir),
	)
	t.Cleanup(mgr.Shutdown)
	mock := &mockSessionAgent{}
	coord := &coordinator{
		cfg:          cfg,
		currentAgent: mock,
		skills:       mgr,
		skillTracker: skills.NewTracker(active),
	}

	skillDir := filepath.Join(skillsRoot, "learned-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(skillDir, skills.SkillFileName),
		[]byte("---\nname: learned-skill\ndescription: First description.\n---\nFULL-INSTRUCTIONS-MUST-STAY-OUT-OF-INDEX\n"),
		0o644,
	))

	require.NoError(t, coord.refreshSkills(t.Context(), "test-provider", "test-model"))
	require.Len(t, mock.systemPrompts, 1)
	require.Contains(t, mock.systemPrompts[0], "<name>learned-skill</name>")
	require.Contains(t, mock.systemPrompts[0], "<description>First description.</description>")
	require.NotContains(t, mock.systemPrompts[0], "FULL-INSTRUCTIONS-MUST-STAY-OUT-OF-INDEX")
	coord.skillTracker.MarkLoaded("learned-skill")
	require.True(t, coord.skillTracker.IsLoaded("learned-skill"))

	require.NoError(t, coord.refreshSkills(t.Context(), "test-provider", "test-model"))
	require.Len(t, mock.systemPrompts, 1, "unchanged turns must reuse the existing prompt")

	require.NoError(t, os.WriteFile(
		filepath.Join(skillDir, skills.SkillFileName),
		[]byte("---\nname: learned-skill\ndescription: Updated description.\n---\nUpdated instructions.\n"),
		0o644,
	))
	require.NoError(t, coord.refreshSkills(t.Context(), "test-provider", "test-model"))
	require.Len(t, mock.systemPrompts, 2)
	require.Contains(t, mock.systemPrompts[1], "<description>Updated description.</description>")
}

func TestRunSubAgent(t *testing.T) {
	const providerID = "test-provider"
	providerCfg := config.ProviderConfig{ID: providerID}

	t.Run("happy path", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
			assert.Equal(t, "do something", call.Prompt)
			assert.Equal(t, int64(4096), call.MaxOutputTokens)
			return agentResultWithText("done"), nil
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "do something",
			SessionTitle:   "Test Session",
		})
		require.NoError(t, err)
		assert.Equal(t, "done", resp.Content)
		assert.False(t, resp.IsError)
	})

	t.Run("cost update failure preserves output", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		agent := newMockAgent(providerID, 4096, func(_ context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
			return agentResultWithText("output before cost failure"), nil
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      "missing-parent-session",
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.NoError(t, err)
		assert.False(t, resp.IsError)
		assert.Equal(t, "output before cost failure", resp.Content)
	})

	t.Run("response with text returns it", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, func(_ context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
			return agentResultWithText("the answer"), nil
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.NoError(t, err)
		assert.False(t, resp.IsError)
		assert.Equal(t, "the answer", resp.Content)
	})

	t.Run("nil result returns error response", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, func(_ context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
			return nil, nil
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Equal(t, "Sub-agent completed but produced no text output.", resp.Content)
	})

	t.Run("empty result returns error response", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, func(_ context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
			return &fantasy.AgentResult{}, nil
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Equal(t, "Sub-agent completed but produced no text output.", resp.Content)
	})

	t.Run("ModelCfg.MaxTokens overrides default", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		agent := &mockSessionAgent{
			model: Model{
				CatwalkCfg: catwalk.Model{
					DefaultMaxTokens: 4096,
				},
				ModelCfg: config.SelectedModel{
					Provider:  providerID,
					MaxTokens: 8192,
				},
			},
			runFunc: func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
				assert.Equal(t, int64(8192), call.MaxOutputTokens)
				return agentResultWithText("ok"), nil
			},
		}

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.NoError(t, err)
		assert.Equal(t, "ok", resp.Content)
	})

	t.Run("session creation failure with canceled context", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, nil)

		// Use a canceled context to trigger CreateTaskSession failure.
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err = coord.runSubAgent(ctx, subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.Error(t, err)
	})

	t.Run("provider not configured", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		// Agent references a provider that doesn't exist in config.
		agent := newMockAgent("unknown-provider", 4096, nil)

		_, err = coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "model provider not configured")
	})

	t.Run("agent run error returns error response", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, func(_ context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
			return nil, errors.New("provider request failed")
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		// runSubAgent returns (errorResponse, nil) when agent.Run fails — not a Go error.
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Equal(t, "Failed to generate response: provider request failed", resp.Content)
	})

	t.Run("session setup callback is invoked", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		var setupCalledWith string
		agent := newMockAgent(providerID, 4096, func(_ context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
			return agentResultWithText("ok"), nil
		})

		_, err = coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
			SessionSetup: func(sessionID string) {
				setupCalledWith = sessionID
			},
		})
		require.NoError(t, err)
		assert.NotEmpty(t, setupCalledWith, "SessionSetup should have been called")
	})

	t.Run("cost propagation to parent session", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
			// Simulate the agent incurring cost by updating the child session.
			childSession, err := env.sessions.Get(ctx, call.SessionID)
			if err != nil {
				return nil, err
			}
			childSession.Cost = 0.05
			_, err = env.sessions.Save(ctx, childSession)
			if err != nil {
				return nil, err
			}
			return agentResultWithText("ok"), nil
		})

		_, err = coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.NoError(t, err)

		updated, err := env.sessions.Get(t.Context(), parentSession.ID)
		require.NoError(t, err)
		assert.InDelta(t, 0.05, updated.Cost, 1e-9)
	})
}

func TestUpdateParentSessionCost(t *testing.T) {
	t.Run("accumulates cost correctly", func(t *testing.T) {
		env := testEnv(t)
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		coord := &coordinator{cfg: cfg, sessions: env.sessions}

		parent, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		child, err := env.sessions.CreateTaskSession(t.Context(), "tool-1", parent.ID, "Child")
		require.NoError(t, err)

		// Set child cost.
		child.Cost = 0.10
		_, err = env.sessions.Save(t.Context(), child)
		require.NoError(t, err)

		err = coord.updateParentSessionCost(t.Context(), child.ID, parent.ID)
		require.NoError(t, err)

		updated, err := env.sessions.Get(t.Context(), parent.ID)
		require.NoError(t, err)
		assert.InDelta(t, 0.10, updated.Cost, 1e-9)
	})

	t.Run("accumulates multiple child costs", func(t *testing.T) {
		env := testEnv(t)
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		coord := &coordinator{cfg: cfg, sessions: env.sessions}

		parent, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		child1, err := env.sessions.CreateTaskSession(t.Context(), "tool-1", parent.ID, "Child1")
		require.NoError(t, err)
		child1.Cost = 0.05
		_, err = env.sessions.Save(t.Context(), child1)
		require.NoError(t, err)

		child2, err := env.sessions.CreateTaskSession(t.Context(), "tool-2", parent.ID, "Child2")
		require.NoError(t, err)
		child2.Cost = 0.03
		_, err = env.sessions.Save(t.Context(), child2)
		require.NoError(t, err)

		err = coord.updateParentSessionCost(t.Context(), child1.ID, parent.ID)
		require.NoError(t, err)
		err = coord.updateParentSessionCost(t.Context(), child2.ID, parent.ID)
		require.NoError(t, err)

		updated, err := env.sessions.Get(t.Context(), parent.ID)
		require.NoError(t, err)
		assert.InDelta(t, 0.08, updated.Cost, 1e-9)
	})

	t.Run("child session not found", func(t *testing.T) {
		env := testEnv(t)
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		coord := &coordinator{cfg: cfg, sessions: env.sessions}

		parent, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		err = coord.updateParentSessionCost(t.Context(), "non-existent", parent.ID)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "get child session")
	})

	t.Run("parent session not found", func(t *testing.T) {
		env := testEnv(t)
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		coord := &coordinator{cfg: cfg, sessions: env.sessions}

		parent, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)
		child, err := env.sessions.CreateTaskSession(t.Context(), "tool-1", parent.ID, "Child")
		require.NoError(t, err)

		err = coord.updateParentSessionCost(t.Context(), child.ID, "non-existent")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "get parent session")
	})

	t.Run("zero cost handled correctly", func(t *testing.T) {
		env := testEnv(t)
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		coord := &coordinator{cfg: cfg, sessions: env.sessions}

		parent, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)
		child, err := env.sessions.CreateTaskSession(t.Context(), "tool-1", parent.ID, "Child")
		require.NoError(t, err)

		err = coord.updateParentSessionCost(t.Context(), child.ID, parent.ID)
		require.NoError(t, err)

		updated, err := env.sessions.Get(t.Context(), parent.ID)
		require.NoError(t, err)
		assert.InDelta(t, 0.0, updated.Cost, 1e-9)
	})
}

func TestGetProviderOptionsReasoningEffort(t *testing.T) {
	// Bedrock is Fantasy's Anthropic under a different provider name; options
	// must land under anthropic.Name so the Anthropic language model picks them up.
	tests := []struct {
		name         string
		providerType catwalk.Type
	}{
		{"anthropic honors reasoning_effort", catwalk.Type(anthropic.Name)},
		{"bedrock honors reasoning_effort", catwalk.Type(bedrock.Name)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model := Model{
				CatwalkCfg: catwalk.Model{
					ID:              "claude-opus-4-7",
					CanReason:       true,
					ReasoningLevels: []string{"max"},
				},
				ModelCfg: config.SelectedModel{
					Provider:        "test",
					ReasoningEffort: "max",
				},
			}
			providerCfg := config.ProviderConfig{ID: "test", Type: tc.providerType}

			opts, err := getProviderOptions(model, providerCfg)
			require.NoError(t, err)

			raw, ok := opts[anthropic.Name]
			require.True(t, ok, "options should be keyed under anthropic.Name for type %q", tc.providerType)
			parsed, ok := raw.(*anthropic.ProviderOptions)
			require.True(t, ok)
			require.NotNil(t, parsed.Effort)
			assert.Equal(t, anthropic.Effort("max"), *parsed.Effort)
		})
	}
}

func TestIsUnauthorized(t *testing.T) {
	t.Run("nil error", func(t *testing.T) {
		assert.False(t, isUnauthorized(nil))
	})

	t.Run("non-provider error", func(t *testing.T) {
		assert.False(t, isUnauthorized(errors.New("something broke")))
	})

	t.Run("provider error with 401", func(t *testing.T) {
		err := &fantasy.ProviderError{StatusCode: http.StatusUnauthorized, Message: "unauthorized"}
		assert.True(t, isUnauthorized(err))
	})

	t.Run("provider error with non-401", func(t *testing.T) {
		err := &fantasy.ProviderError{StatusCode: http.StatusForbidden, Message: "forbidden"}
		assert.False(t, isUnauthorized(err))
	})

	t.Run("wrapped provider error with 401", func(t *testing.T) {
		inner := &fantasy.ProviderError{StatusCode: http.StatusUnauthorized, Message: "expired"}
		err := fmt.Errorf("request failed: %w", inner)
		assert.True(t, isUnauthorized(err))
	})
}

func TestGetProviderOptionsReasoningEffortFallback(t *testing.T) {
	model := Model{
		CatwalkCfg: catwalk.Model{
			ID:              "glm-5.2",
			CanReason:       true,
			ReasoningLevels: []string{"high", "max"},
		},
		ModelCfg: config.SelectedModel{
			Provider: "zai",
		},
	}
	providerCfg := config.ProviderConfig{
		ID:   string(catwalk.InferenceProviderZAI),
		Type: openaicompat.Name,
	}

	opts, err := getProviderOptions(model, providerCfg)
	require.NoError(t, err)

	raw, ok := opts[openaicompat.Name]
	require.True(t, ok)
	parsed, ok := raw.(*openaicompat.ProviderOptions)
	require.True(t, ok)
	require.NotNil(t, parsed.ReasoningEffort)
	assert.Equal(t, "high", string(*parsed.ReasoningEffort))

	thinking, ok := parsed.ExtraBody["thinking"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "enabled", thinking["type"])
}

func TestMergeResponsesIncludes(t *testing.T) {
	tests := []struct {
		name       string
		configured any
		required   []openai.IncludeType
		want       []openai.IncludeType
		wantErr    bool
	}{
		{"nil adds required", nil, []openai.IncludeType{openai.IncludeReasoningEncryptedContent},
			[]openai.IncludeType{openai.IncludeReasoningEncryptedContent}, false},
		{"empty slice adds required", []any{}, []openai.IncludeType{openai.IncludeReasoningEncryptedContent},
			[]openai.IncludeType{openai.IncludeReasoningEncryptedContent}, false},
		{"user values preserved and unioned", []any{"file_search_call.results", "logprobs"}, []openai.IncludeType{openai.IncludeReasoningEncryptedContent},
			[]openai.IncludeType{"file_search_call.results", "logprobs", openai.IncludeReasoningEncryptedContent}, false},
		{"duplicates deduped", []any{string(openai.IncludeReasoningEncryptedContent), "reasoning.encrypted_content"}, []openai.IncludeType{openai.IncludeReasoningEncryptedContent},
			[]openai.IncludeType{openai.IncludeReasoningEncryptedContent}, false},
		{"string slice accepted", []string{"file_search_call.results"}, []openai.IncludeType{openai.IncludeReasoningEncryptedContent},
			[]openai.IncludeType{"file_search_call.results", openai.IncludeReasoningEncryptedContent}, false},
		{"typed slice accepted", []openai.IncludeType{"file_search_call.results"}, []openai.IncludeType{openai.IncludeReasoningEncryptedContent},
			[]openai.IncludeType{"file_search_call.results", openai.IncludeReasoningEncryptedContent}, false},
		{"empty strings dropped", []any{"", ""}, []openai.IncludeType{openai.IncludeReasoningEncryptedContent},
			[]openai.IncludeType{openai.IncludeReasoningEncryptedContent}, false},
		{"output sorted lexically", []any{"z.include", "a.include"}, nil,
			[]openai.IncludeType{"a.include", "z.include"}, false},
		{"non-string entry rejected", []any{42}, nil, nil, true},
		{"non-array rejected", "file_search_call.results", nil, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mergeResponsesIncludes(tc.configured, tc.required...)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func responsesModel() Model {
	return Model{
		CatwalkCfg: catwalk.Model{
			ID:        "gpt-5.2",
			CanReason: true,
		},
		ModelCfg: config.SelectedModel{Provider: "openai"},
	}
}

func TestGetProviderOptionsReasoningSummarySemantics(t *testing.T) {
	providerCfg := config.ProviderConfig{ID: "openai", Type: openai.Name}

	t.Run("default auto when absent", func(t *testing.T) {
		opts, err := getProviderOptions(responsesModel(), providerCfg)
		require.NoError(t, err)
		parsed, ok := opts[openai.Name].(*openai.ResponsesProviderOptions)
		require.True(t, ok)
		require.NotNil(t, parsed.ReasoningSummary)
		require.Equal(t, "auto", *parsed.ReasoningSummary)
	})

	for _, userValue := range []string{"auto", "concise", "detailed"} {
		t.Run("user value wins: "+userValue, func(t *testing.T) {
			model := responsesModel()
			model.ModelCfg.ProviderOptions = map[string]any{"reasoning_summary": userValue}
			opts, err := getProviderOptions(model, providerCfg)
			require.NoError(t, err)
			parsed := opts[openai.Name].(*openai.ResponsesProviderOptions)
			require.NotNil(t, parsed.ReasoningSummary)
			require.Equal(t, userValue, *parsed.ReasoningSummary)
		})
	}

	t.Run("explicit user null omits summary", func(t *testing.T) {
		model := responsesModel()
		model.ModelCfg.ProviderOptions = map[string]any{"reasoning_summary": nil}
		opts, err := getProviderOptions(model, providerCfg)
		require.NoError(t, err)
		parsed := opts[openai.Name].(*openai.ResponsesProviderOptions)
		require.Nil(t, parsed.ReasoningSummary, "explicit null must omit, not restore auto")
	})

	for _, invalid := range []string{"none", "verbose", ""} {
		t.Run("invalid value fails: '"+invalid+"'", func(t *testing.T) {
			model := responsesModel()
			model.ModelCfg.ProviderOptions = map[string]any{"reasoning_summary": invalid}
			_, err := getProviderOptions(model, providerCfg)
			require.Error(t, err)
			require.Contains(t, err.Error(), "reasoning_summary")
		})
	}
}

func TestGetProviderOptionsInvalidIncludeFails(t *testing.T) {
	providerCfg := config.ProviderConfig{ID: "openai", Type: openai.Name}

	t.Run("non-string include entry", func(t *testing.T) {
		model := responsesModel()
		model.ModelCfg.ProviderOptions = map[string]any{"include": []any{"file_search_call.results", 7}}
		_, err := getProviderOptions(model, providerCfg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "include")
	})

	t.Run("non-array include", func(t *testing.T) {
		model := responsesModel()
		model.ModelCfg.ProviderOptions = map[string]any{"include": "file_search_call.results"}
		_, err := getProviderOptions(model, providerCfg)
		require.Error(t, err)
	})

	t.Run("valid user includes survive union", func(t *testing.T) {
		model := responsesModel()
		model.ModelCfg.ProviderOptions = map[string]any{"include": []any{"file_search_call.results"}}
		opts, err := getProviderOptions(model, providerCfg)
		require.NoError(t, err)
		parsed := opts[openai.Name].(*openai.ResponsesProviderOptions)
		require.Equal(t, []openai.IncludeType{"file_search_call.results", openai.IncludeReasoningEncryptedContent}, parsed.Include)
	})
}

func TestMergeCallOptionsPropagatesError(t *testing.T) {
	providerCfg := config.ProviderConfig{ID: "openai", Type: openai.Name}
	model := responsesModel()
	model.ModelCfg.ProviderOptions = map[string]any{"reasoning_summary": "none"}

	opts, _, _, _, _, _, err := mergeCallOptions(model, providerCfg)
	require.Error(t, err)
	require.Nil(t, opts, "no options must be returned on failure so no degraded request can be built")
}

func TestGetProviderOptionsUserNullBeatsCatwalkDefault(t *testing.T) {
	providerCfg := config.ProviderConfig{ID: "openai", Type: openai.Name}
	model := responsesModel()
	model.CatwalkCfg.Options.ProviderOptions = map[string]any{"reasoning_summary": "auto"}
	model.ModelCfg.ProviderOptions = map[string]any{"reasoning_summary": nil}

	opts, err := getProviderOptions(model, providerCfg)
	require.NoError(t, err)
	parsed := opts[openai.Name].(*openai.ResponsesProviderOptions)
	require.Nil(t, parsed.ReasoningSummary, "user explicit null must omit summary even when catwalk defaults to auto")
}

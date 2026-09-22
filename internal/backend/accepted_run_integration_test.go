package backend

import (
	"context"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/openaicompat"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// gatedCoordinator wraps a real agent.Coordinator and parks RunAccepted
// before delegating to it. Every method other than RunAccepted is
// inherited from the embedded coordinator, so BeginAccepted (called by
// Backend.SendMessage) and RunAccepted (called by the dispatched run)
// are the production agent.Coordinator implementations under test, not
// stubs. The gate only delays entry into the real RunAccepted so a
// cancel can be made to land in the accepted-but-not-yet-active window
// deterministically: the accept handle is not consumed by
// sessionAgent.Run until the real RunAccepted runs after the gate opens.
type gatedCoordinator struct {
	agent.Coordinator
	entered chan struct{}
	gate    chan struct{}
}

func (c *gatedCoordinator) RunAccepted(ctx context.Context, accept *agent.AcceptedRun, sessionID, prompt string, attachments ...message.Attachment) (*fantasy.AgentResult, error) {
	close(c.entered)
	<-c.gate
	return c.Coordinator.RunAccepted(ctx, accept, sessionID, prompt, attachments...)
}

// newTestCoordinator builds a real agent.Coordinator through the
// production agent.NewCoordinator constructor so the RunAccepted /
// BeginAccepted / run path is the actual code under test.
//
// It installs a minimal config with a single openai-compatible provider
// whose model resolves offline. run rebuilds the model on every call, so
// the provider must construct without network I/O; the cancel-on-entry
// path this test exercises returns before any model call, so no request
// is ever issued. The coder agent's allowed-tools list is cleared to
// keep tool construction cheap and free of sub-agent wiring. The
// optional coordinator dependencies (history, filetracker, LSP, notify,
// runComplete, skills) are nil: run guards the publisher fields and the
// cancel-on-entry path never touches the others.
func newTestCoordinator(ctx context.Context, workingDir string, sessions session.Service, messages message.Service) (agent.Coordinator, error) {
	cfg, err := config.Init(workingDir, "", false)
	if err != nil {
		return nil, err
	}

	const (
		providerID = "test-openai-compat"
		modelID    = "test-model"
	)
	cfg.Config().Providers.Set(providerID, config.ProviderConfig{
		ID:      providerID,
		Name:    "Test",
		Type:    openaicompat.Name,
		BaseURL: "http://127.0.0.1:0/v1",
		APIKey:  "test",
		Models:  []catwalk.Model{{ID: modelID, DefaultMaxTokens: 4096}},
	})
	selected := config.SelectedModel{Provider: providerID, Model: modelID}
	cfg.OverridePreferredModel(config.SelectedModelTypeLarge, selected)
	cfg.OverridePreferredModel(config.SelectedModelTypeSmall, selected)
	cfg.SetupAgents()

	// Keep buildTools light: no sub-agent or agentic-fetch construction.
	coderCfg := cfg.Config().Agents[config.AgentCoder]
	coderCfg.AllowedTools = nil
	cfg.Config().Agents[config.AgentCoder] = coderCfg

	return agent.NewCoordinator(ctx, agent.CoordinatorOptions{
		Config:   cfg,
		Sessions: sessions,
		Messages: messages,
	})
}

// newRealCoordinator builds a production agent.Coordinator over a
// DB-backed session/message store, wrapped in a gate. It is constructed
// through the real agent.NewCoordinator path with an offline-resolvable
// model: the cancel-on-entry path under test persists a canceled turn
// and returns before any model call, so no network I/O happens.
func newRealCoordinator(t *testing.T) (*gatedCoordinator, session.Service, message.Service) {
	t.Helper()
	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	q := db.New(conn)
	sessions := session.NewService(q, conn)
	messages := message.NewService(q)

	coord, err := newTestCoordinator(t.Context(), t.TempDir(), sessions, messages)
	require.NoError(t, err)

	return &gatedCoordinator{
		Coordinator: coord,
		entered:     make(chan struct{}),
		gate:        make(chan struct{}),
	}, sessions, messages
}

// TestSendMessage_AcceptedCancelRace_RealMachinery exercises the
// 202/cancel race end-to-end through Backend.SendMessage against the
// production agent.Coordinator (BeginAccepted + RunAccepted), not a
// stub. It asserts that a cancel arriving after the prompt is accepted
// but before the run becomes active is not lost: the accepted handle
// reaches sessionAgent.Run and drives cancel-on-entry, which persists a
// canceled turn instead of streaming.
//
// This test would fail if Coordinator.BeginAccepted returned nil (Cancel
// would find no accepted run and record no mark, and the run would
// receive a nil Accepted handle and skip cancel-on-entry) or if
// Coordinator.RunAccepted dropped the handle on its way into
// sessionAgent.Run (the run would likewise skip cancel-on-entry and try
// to stream the model). In either case no FinishReasonCanceled turn
// would be persisted.
func TestSendMessage_AcceptedCancelRace_RealMachinery(t *testing.T) {
	t.Parallel()
	b, _ := newTestBackend(t)

	coord, sessions, messages := newRealCoordinator(t)
	sess, err := sessions.Create(t.Context(), "session")
	require.NoError(t, err)

	ws := insertAgentWorkspace(t, b, coord)

	require.NoError(t, b.SendMessage(ws.ID, proto.AgentMessage{SessionID: sess.ID, Prompt: "hi"}))

	// Coordinator.BeginAccepted ran synchronously inside SendMessage
	// before dispatch; the dispatched run has now entered the gate but
	// has not yet called the real RunAccepted, so the accept handle is
	// not yet consumed: the prompt is accepted but not active.
	select {
	case <-coord.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatched run never entered RunAccepted")
	}

	// A cancel arriving now lands in the accepted-but-not-yet-active
	// window and is only recorded because BeginAccepted incremented the
	// accept counter.
	require.NoError(t, b.CancelSession(ws.ID, sess.ID))

	// Release the gate so the real RunAccepted threads the handle into
	// sessionAgent.Run, which drives cancel-on-entry.
	close(coord.gate)

	// The dispatched run returns nil (cancel-on-entry), so runWG drains.
	waited := make(chan struct{})
	go func() {
		ws.runWG.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("runWG.Wait did not complete after the canceled run returned")
	}

	// The accepted-but-not-yet-active cancel persisted a canceled turn
	// rather than streaming a real response.
	msgs, err := messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	require.Equal(t, message.User, msgs[0].Role)
	require.Equal(t, message.Assistant, msgs[1].Role)
	require.Equal(t, message.FinishReasonCanceled, msgs[1].FinishReason())
}

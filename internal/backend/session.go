package backend

import (
	"context"
	"errors"
	"fmt"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/session"
)

// CreateSession creates a new session in the given workspace.
func (b *Backend) CreateSession(ctx context.Context, workspaceID, title string) (session.Session, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return session.Session{}, err
	}

	return ws.Sessions.Create(ctx, title)
}

// GetSession retrieves a session by workspace and session ID.
func (b *Backend) GetSession(ctx context.Context, workspaceID, sessionID string) (session.Session, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return session.Session{}, err
	}

	return ws.Sessions.Get(ctx, sessionID)
}

// ListSessions returns all sessions in the given workspace.
func (b *Backend) ListSessions(ctx context.Context, workspaceID string) ([]session.Session, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Sessions.List(ctx)
}

// GetAgentSession returns session metadata with the agent's busy
// status.
func (b *Backend) GetAgentSession(ctx context.Context, workspaceID, sessionID string) (proto.AgentSession, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return proto.AgentSession{}, err
	}

	se, err := ws.Sessions.Get(ctx, sessionID)
	if err != nil {
		return proto.AgentSession{}, err
	}

	var isSessionBusy bool
	if ws.AgentCoordinator != nil {
		isSessionBusy = ws.AgentCoordinator.IsSessionBusy(sessionID)
	}

	return proto.AgentSession{
		Session: proto.Session{
			ID:    se.ID,
			Title: se.Title,
		},
		IsBusy: isSessionBusy,
	}, nil
}

// ListSessionMessages returns all messages for a session.
func (b *Backend) ListSessionMessages(ctx context.Context, workspaceID, sessionID string) ([]message.Message, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	// Drain debounced updates so HTTP clients (and the TUI on session
	// switch) observe the latest in-memory state rather than racing the
	// debounce timer in message.Service.
	if err := ws.Messages.FlushAll(ctx); err != nil {
		return nil, err
	}
	return ws.Messages.List(ctx, sessionID)
}

// ListSessionHistory returns the history items for a session.
func (b *Backend) ListSessionHistory(ctx context.Context, workspaceID, sessionID string) (any, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.History.ListBySession(ctx, sessionID)
}

// SaveSession updates a session in the given workspace.
func (b *Backend) SaveSession(ctx context.Context, workspaceID string, sess session.Session) (session.Session, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return session.Session{}, err
	}

	return ws.Sessions.Save(ctx, sess)
}

// DeleteSession deletes a session from the given workspace.
func (b *Backend) DeleteSession(ctx context.Context, workspaceID, sessionID string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	return ws.Sessions.Delete(ctx, sessionID)
}

// ListUserMessages returns user-role messages for a session.
func (b *Backend) ListUserMessages(ctx context.Context, workspaceID, sessionID string) ([]message.Message, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Messages.ListUserMessages(ctx, sessionID)
}

// ListAllUserMessages returns all user-role messages across sessions.
func (b *Backend) ListAllUserMessages(ctx context.Context, workspaceID string) ([]message.Message, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Messages.ListAllUserMessages(ctx)
}

// CloneSession creates a child session with an exact copy of the source transcript.
// Usage counters reset because the clone has not spent model tokens yet.
func (b *Backend) CloneSession(ctx context.Context, workspaceID, sessionID string) (session.Session, error) {
	return b.copySession(ctx, workspaceID, sessionID, "")
}

// ForkSession creates a child session whose transcript ends at the selected user message.
func (b *Backend) ForkSession(ctx context.Context, workspaceID, sessionID, messageID string) (session.Session, error) {
	if messageID == "" {
		return session.Session{}, errors.New("message id is required")
	}
	return b.copySession(ctx, workspaceID, sessionID, messageID)
}

func (b *Backend) copySession(ctx context.Context, workspaceID, sessionID, throughMessageID string) (result session.Session, err error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return session.Session{}, err
	}
	if ws.AgentCoordinator != nil && ws.AgentCoordinator.IsSessionBusy(sessionID) {
		return session.Session{}, errors.New("cannot copy a session while it is running")
	}
	if err := ws.Messages.FlushAll(ctx); err != nil {
		return session.Session{}, err
	}
	source, err := ws.Sessions.Get(ctx, sessionID)
	if err != nil {
		return session.Session{}, err
	}
	messages, err := ws.Messages.List(ctx, sessionID)
	if err != nil {
		return session.Session{}, err
	}
	limit := len(messages)
	if throughMessageID != "" {
		limit = -1
		for i, msg := range messages {
			if msg.ID == throughMessageID {
				if msg.Role != message.User {
					return session.Session{}, errors.New("fork point must be a user message")
				}
				limit = i + 1
				break
			}
		}
		if limit < 0 {
			return session.Session{}, fmt.Errorf("fork message %q not found", throughMessageID)
		}
	}

	child, err := ws.Sessions.CreateChild(ctx, source.ID, source.Title)
	if err != nil {
		return session.Session{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = ws.Sessions.Delete(context.WithoutCancel(ctx), child.ID)
		}
	}()
	for _, msg := range messages[:limit] {
		cloned, cloneErr := ws.Messages.CloneToSession(ctx, child.ID, msg)
		if cloneErr != nil {
			return session.Session{}, cloneErr
		}
		if msg.ID == source.SummaryMessageID {
			child.SummaryMessageID = cloned.ID
		}
	}
	child.PromptTokens = 0
	child.CompletionTokens = 0
	child.Cost = 0
	child.EstimatedUsage = false
	child.Todos = nil
	child, err = ws.Sessions.Save(ctx, child)
	if err != nil {
		return session.Session{}, err
	}
	committed = true
	return child, nil
}

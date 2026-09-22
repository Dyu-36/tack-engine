//go:build gotacktest

// Test-only lifecycle overrides. Compiled only with the gotacktest build tag
// so the shipped engine binary never carries them; tests shorten the default
// windows instead of waiting them out.
package backend

import "time"

// SetCreateGrace overrides the window in which a client must open its
// first SSE stream after creating a workspace before its claim is
// released. A value <= 0 falls back to [DefaultCreateGrace] on the next
// New call, not here. Intended for deployments and tests that need a
// different lifecycle window than the default.
func (b *Backend) SetCreateGrace(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.createGrace = d
}

// SetDetachGrace overrides how long a client's claim survives after its
// last SSE stream drops. A value <= 0 restores the tear-down-immediately
// behavior.
func (b *Backend) SetDetachGrace(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.detachGrace = d
}

// SetIdleShutdownDelay overrides how long the server lingers after its
// last workspace is released before shutting down. A value <= 0 restores
// the shut-down-immediately behavior.
func (b *Backend) SetIdleShutdownDelay(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lingerDelay = d
}

// AttachedClients returns the number of clients currently viewing
// sessionID in the given workspace. Only clients with at least one live
// SSE stream (streams > 0) AND a matching currentSessionID are counted;
// pure creation holds do not contribute. Returns [ErrWorkspaceNotFound]
// if the workspace is unknown.
func (b *Backend) AttachedClients(workspaceID, sessionID string) (int, error) {
	ws, ok := b.workspaces.Get(workspaceID)
	if !ok {
		return 0, ErrWorkspaceNotFound
	}
	return ws.AttachedClientsForSession(sessionID), nil
}

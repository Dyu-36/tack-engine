package agent

import (
	"context"
	"testing"
	"time"

	"charm.land/fantasy"
)

type cancellingTitleModel struct {
	finishStreamModel
	started chan struct{}
	stopped chan struct{}
}

func (m *cancellingTitleModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	close(m.started)
	<-ctx.Done()
	close(m.stopped)
	return nil, ctx.Err()
}

func TestRunJoinsCancelledTitleBeforeReturning(t *testing.T) {
	env := testEnv(t)
	title := &cancellingTitleModel{started: make(chan struct{}), stopped: make(chan struct{})}
	main := &titleAwaitingMainModel{finishStreamModel: finishStreamModel{text: "done"}, started: title.started}
	sa := testSessionAgent(env, main, title, "system")
	session, err := env.sessions.Create(t.Context(), "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := sa.Run(ctx, SessionAgentCall{SessionID: session.ID, Prompt: "test"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-title.stopped:
	default:
		t.Fatal("Run returned while title goroutine was still using session storage")
	}
}

type titleAwaitingMainModel struct {
	finishStreamModel
	started <-chan struct{}
}

func (m *titleAwaitingMainModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	select {
	case <-m.started:
		return m.finishStreamModel.Stream(ctx, call)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

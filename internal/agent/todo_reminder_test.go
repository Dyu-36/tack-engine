package agent

import (
	"strings"
	"testing"

	"charm.land/fantasy/providers/openai"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

func TestRenderTodoReminder(t *testing.T) {
	t.Run("empty list says empty", func(t *testing.T) {
		out := renderTodoReminder(nil)
		require.Contains(t, out, "<state>empty</state>")
		require.NotContains(t, out, "<todo ")
	})

	t.Run("mixed statuses keep session order", func(t *testing.T) {
		// Order is z-first on purpose: re-sorting by status or content
		// would erase the model's priority order.
		todos := []session.Todo{
			{Content: "zeta task", Status: session.TodoStatusInProgress},
			{Content: "alpha task", Status: session.TodoStatusCompleted},
			{Content: "middle task", Status: session.TodoStatusPending},
		}
		out := renderTodoReminder(todos)
		zeta := strings.Index(out, "zeta task")
		alpha := strings.Index(out, "alpha task")
		middle := strings.Index(out, "middle task")
		require.GreaterOrEqual(t, zeta, 0)
		require.GreaterOrEqual(t, alpha, zeta, "session order must be preserved")
		require.GreaterOrEqual(t, middle, alpha)
		require.Contains(t, out, `<todo status="in_progress">zeta task</todo>`)
		require.Contains(t, out, "<state>active</state>")
		require.NotContains(t, out, "currently empty")
		require.Contains(t, out, "<total>3</total>")
	})

	t.Run("never claims empty when tasks exist", func(t *testing.T) {
		todos := []session.Todo{{Content: "only task", Status: session.TodoStatusPending}}
		out := renderTodoReminder(todos)
		require.NotContains(t, out, "<state>empty</state>")
		require.Contains(t, out, "only task")
	})

	t.Run("all completed reported", func(t *testing.T) {
		todos := []session.Todo{
			{Content: "one", Status: session.TodoStatusCompleted},
			{Content: "two", Status: session.TodoStatusCompleted},
		}
		out := renderTodoReminder(todos)
		require.Contains(t, out, "<state>all-completed</state>")
		require.NotContains(t, out, "<state>active</state>")
	})

	t.Run("XML special characters escaped", func(t *testing.T) {
		todos := []session.Todo{{
			Content: `fix <a href="x">&y</a> 'quote'`,
			Status:  session.TodoStatusPending,
		}}
		out := renderTodoReminder(todos)
		require.Contains(t, out, "fix &lt;a href=&quot;x&quot;&gt;&amp;y&lt;/a&gt; &apos;quote&apos;</todo>")
		require.NotContains(t, out, `<a href=`)
	})

	t.Run("task cap reports omitted count", func(t *testing.T) {
		var todos []session.Todo
		for i := 0; i < maxTodoReminderTasks+10; i++ {
			todos = append(todos, session.Todo{
				Content: "task",
				Status:  session.TodoStatusPending,
			})
		}
		out := renderTodoReminder(todos)
		require.Contains(t, out, "<truncated>true</truncated>")
		require.Contains(t, out, "<omitted>10</omitted>")
		require.Contains(t, out, "<total>74</total>")
		require.Contains(t, out, "omitted-status")
	})

	t.Run("byte cap truncates whole items", func(t *testing.T) {
		var todos []session.Todo
		for i := 0; i < 200; i++ {
			todos = append(todos, session.Todo{
				Content: strings.Repeat("x", 512),
				Status:  session.TodoStatusPending,
			})
		}
		out := renderTodoReminder(todos)
		require.LessOrEqual(t, len(out), maxTodoReminderBytes)
		require.Contains(t, out, "<truncated>true</truncated>")
		require.Contains(t, out, "<total>200</total>")
		// No partial item: every rendered item is a complete element.
		require.Equal(t, strings.Count(out, "<todo "), strings.Count(out, "</todo>"))
	})
}

func TestPreparePromptForModelRejectsDuplicateResponsesItemIDs(t *testing.T) {
	agent := &sessionAgent{isSubAgent: false}
	encrypted := "cipher"
	first := &message.Message{
		ID:       "msg-1",
		Role:     message.Assistant,
		Provider: "openai",
		Model:    "gpt-5.2",
	}
	first.AppendReasoningContentForID("evt-1", "thinking")
	first.SetReasoningResponsesDataForID("evt-1", &openai.ResponsesReasoningMetadata{ItemID: "rs_dup", EncryptedContent: &encrypted})

	second := &message.Message{
		ID:       "msg-2",
		Role:     message.Assistant,
		Provider: "openai",
		Model:    "gpt-5.2",
	}
	second.AppendReasoningContentForID("evt-2", "more")
	second.SetReasoningResponsesDataForID("evt-2", &openai.ResponsesReasoningMetadata{ItemID: "rs_dup", EncryptedContent: &encrypted})

	_, _, err := agent.preparePromptForModel([]message.Message{*first, *second}, true, "openai", "gpt-5.2")
	require.Error(t, err, "duplicate item_id must fail before any network call")
	require.Contains(t, err.Error(), "duplicate reasoning item_id")

	// Distinct item IDs pass.
	second.SetReasoningResponsesDataForID("evt-2", &openai.ResponsesReasoningMetadata{ItemID: "rs_ok", EncryptedContent: &encrypted})
	_, _, err = agent.preparePromptForModel([]message.Message{*first, *second}, true, "openai", "gpt-5.2")
	require.NoError(t, err)
}

func TestPreparePromptForModelStripsCrossModelResponsesData(t *testing.T) {
	agent := &sessionAgent{isSubAgent: false}
	encrypted := "cipher-a"
	fromOtherModel := &message.Message{
		ID:       "msg-1",
		Role:     message.Assistant,
		Provider: "openai",
		Model:    "gpt-4.1",
	}
	fromOtherModel.AppendReasoningContentForID("evt-1", "thought")
	fromOtherModel.SetReasoningResponsesDataForID("evt-1", &openai.ResponsesReasoningMetadata{ItemID: "rs_a", EncryptedContent: &encrypted})

	history, _, err := agent.preparePromptForModel([]message.Message{*fromOtherModel}, true, "openai", "gpt-5.2")
	require.NoError(t, err)
	require.NotEmpty(t, history)

	// The message itself is not mutated (clone semantics).
	require.NotNil(t, fromOtherModel.ReasoningContents()[0].ResponsesData)
}

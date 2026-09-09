package agent

import (
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// historyFixtures builds messages: u(text), a(reasoning+calls, callIDs...),
// t(results, resultIDs...), plus summary messages.
func userMsg(id, text string) message.Message {
	return message.Message{ID: id, Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: text}}}
}

func assistantMsg(id string, thinking string, responsesData *message.ResponsesReasoningData, calls ...message.ToolCall) message.Message {
	parts := []message.ContentPart{}
	if thinking != "" || responsesData != nil {
		parts = append(parts, message.ReasoningContent{Thinking: thinking, ResponsesData: responsesData})
	}
	for _, call := range calls {
		parts = append(parts, call)
	}
	return message.Message{ID: id, Role: message.Assistant, Parts: parts}
}

func toolResultMsg(id string, results ...message.ToolResult) message.Message {
	parts := make([]message.ContentPart, 0, len(results))
	for _, result := range results {
		parts = append(parts, result)
	}
	return message.Message{ID: id, Role: message.Tool, Parts: parts}
}

func call(id string) message.ToolCall {
	return message.ToolCall{ID: id, Name: "view", Input: "{}", Finished: true}
}

func result(id string) message.ToolResult {
	return message.ToolResult{ToolCallID: id, Name: "view", Content: "ok"}
}

func summaryMsg(id string) message.Message {
	return message.Message{
		ID:               id,
		Role:             message.Assistant,
		IsSummaryMessage: true,
		Parts:            []message.ContentPart{message.TextContent{Text: "<conversation_summary>earlier work</conversation_summary>"}},
	}
}

func ids(msgs []message.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.ID)
	}
	return out
}

func TestSelectHistoryWithoutSummaryIsUnchanged(t *testing.T) {
	msgs := []message.Message{
		userMsg("u1", "hello"),
		assistantMsg("a1", "", nil),
	}
	require.Equal(t, []string{"u1", "a1"}, ids(selectHistoryWithAnchor(msgs, "")))
}

func TestSelectHistoryKeepsCommittedSummaryAsUser(t *testing.T) {
	anchor := assistantMsg("a1", "thinking-1", &message.ResponsesReasoningData{ItemID: "item-1"})
	msgs := []message.Message{
		userMsg("u1", "old prompt"),
		anchor,
		summaryMsg("s1"),
		userMsg("u2", "after compaction"),
	}
	got := selectHistoryWithAnchor(msgs, "s1")
	require.Equal(t, []string{"a1", "s1", "u2"}, ids(got))
	require.Equal(t, message.User, got[1].Role, "committed summary renders as user context")
}

func TestSelectHistoryRetainsLatestCompleteAnchorGroup(t *testing.T) {
	olderAnchor := assistantMsg("a1", "thinking-1", &message.ResponsesReasoningData{ItemID: "item-1"}, call("c1"))
	olderResult := toolResultMsg("t1", result("c1"))
	newerAnchor := assistantMsg("a2", "thinking-2", &message.ResponsesReasoningData{ItemID: "item-2"}, call("c2"))
	newerResult := toolResultMsg("t2", result("c2"))
	msgs := []message.Message{
		userMsg("u1", "old"),
		olderAnchor,
		olderResult,
		userMsg("u2", "middle"),
		newerAnchor,
		newerResult,
		summaryMsg("s1"),
		userMsg("u3", "new"),
	}
	got := selectHistoryWithAnchor(msgs, "s1")
	// The LATEST complete anchor group is retained with its reasoning,
	// tool call and result; the older group stays compacted away.
	require.Equal(t, []string{"a2", "t2", "s1", "u3"}, ids(got))
	require.Equal(t, message.User, got[2].Role)
}

func TestSelectHistorySkipsIncompleteGroupAndKeepsEarlierCompleteOne(t *testing.T) {
	completeAnchor := assistantMsg("a1", "thinking-1", &message.ResponsesReasoningData{ItemID: "item-1"}, call("c1"))
	completeResult := toolResultMsg("t1", result("c1"))
	cancelled := assistantMsg("a2", "thinking-2", &message.ResponsesReasoningData{ItemID: "item-2"}, call("c2"))
	// c2 has no result: the run was cancelled between the call and the
	// result, so this group is incomplete and must not anchor.
	msgs := []message.Message{
		userMsg("u1", "old"),
		completeAnchor,
		completeResult,
		cancelled,
		summaryMsg("s1"),
		userMsg("u2", "new"),
	}
	got := selectHistoryWithAnchor(msgs, "s1")
	require.Equal(t, []string{"a1", "t1", "s1", "u2"}, ids(got))
}

func TestSelectHistoryNoCompleteAnchorKeepsSummaryOnly(t *testing.T) {
	cancelled := assistantMsg("a2", "thinking-2", &message.ResponsesReasoningData{ItemID: "item-2"}, call("c2"))
	msgs := []message.Message{
		userMsg("u1", "old"),
		cancelled,
		summaryMsg("s1"),
		userMsg("u2", "new"),
	}
	got := selectHistoryWithAnchor(msgs, "s1")
	require.Equal(t, []string{"s1", "u2"}, ids(got))
}

func TestSelectHistoryPlainAssistantAnchorWithoutCalls(t *testing.T) {
	plain := assistantMsg("a1", "thinking-1", &message.ResponsesReasoningData{ItemID: "item-1"})
	msgs := []message.Message{
		userMsg("u1", "old"),
		plain,
		summaryMsg("s1"),
		userMsg("u2", "new"),
	}
	got := selectHistoryWithAnchor(msgs, "s1")
	require.Equal(t, []string{"a1", "s1", "u2"}, ids(got))
}

func TestSelectHistoryDropsUncommittedSummaryMessages(t *testing.T) {
	// Crash recovery: the summary message was created but the session
	// save (the commit pointer) never happened. The orphan must not
	// enter the history as an assistant message.
	anchor := assistantMsg("a1", "thinking-1", &message.ResponsesReasoningData{ItemID: "item-1"})
	orphan := summaryMsg("orphan-summary")
	msgs := []message.Message{
		userMsg("u1", "prompt"),
		anchor,
		orphan,
		userMsg("u2", "next"),
	}
	got := selectHistoryWithAnchor(msgs, "")
	require.Equal(t, []string{"u1", "a1", "u2"}, ids(got),
		"uncommitted summary must be excluded, never replayed")
}

func TestSelectHistoryAnchorNeverDuplicatesOrOrphans(t *testing.T) {
	// Two encrypted-only reasoning items in one anchor turn with two
	// calls and both results: nothing may be duplicated or orphaned.
	anchor := assistantMsg("a1", "", &message.ResponsesReasoningData{ItemID: "item-1"}, call("c1"))
	anchor.Parts = append(anchor.Parts,
		message.ReasoningContent{ResponsesData: &message.ResponsesReasoningData{ItemID: "item-2"}},
		call("c2"))
	results := toolResultMsg("t1", result("c1"), result("c2"))
	msgs := []message.Message{
		userMsg("u1", "old"),
		anchor,
		results,
		summaryMsg("s1"),
		userMsg("u2", "new"),
	}
	got := selectHistoryWithAnchor(msgs, "s1")
	require.Equal(t, []string{"a1", "t1", "s1", "u2"}, ids(got))
	seen := map[string]int{}
	for _, m := range got {
		for _, part := range m.Parts {
			if reasoning, ok := part.(message.ReasoningContent); ok && reasoning.ResponsesData != nil {
				seen[reasoning.ResponsesData.ItemID]++
			}
		}
	}
	require.Equal(t, map[string]int{"item-1": 1, "item-2": 1},
		seen, "each reasoning item replays exactly once")
}

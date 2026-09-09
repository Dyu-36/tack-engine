package agent

import (
	"github.com/charmbracelet/crush/internal/message"
)

// This file implements the bounded PR5 history-selection contract
// (ImplementPlan 0.6 / docs/contracts/openai-reasoning-continuity.md).
// It is intentionally the only place that decides which messages of a
// summarized session reach the model; the LLM summary algorithm,
// thresholds and summary role are unchanged.

// selectHistoryWithAnchor applies the compaction history contract:
//
//  1. Summary messages that are not the committed summary pointer are
//     dropped. Session.SummaryMessageID is the commit point of a
//     compaction; a crash between summary-message creation and the
//     session save leaves an orphaned summary message that must never
//     enter the model history (no half-committed state).
//  2. The committed summary message is kept, sliced to the front of the
//     history and forced into the user role (the existing compaction
//     contract).
//  3. The latest complete valid assistant anchor group from the
//     compacted-away prefix is retained, in its original chronological
//     position before the summary: the assistant message plus the
//     tool-result messages that follow it, so ordered reasoning parts,
//     their function calls and corresponding results survive the
//     boundary together. Groups are never split, duplicated or orphaned.
func selectHistoryWithAnchor(msgs []message.Message, summaryID string) []message.Message {
	committed := 0
	for _, m := range msgs {
		if m.IsSummaryMessage && m.ID != summaryID {
			continue
		}
		msgs[committed] = m
		committed++
	}
	msgs = msgs[:committed]

	if summaryID == "" {
		return msgs
	}
	summaryMsgIndex := -1
	for i, msg := range msgs {
		if msg.ID == summaryID {
			summaryMsgIndex = i
			break
		}
	}
	if summaryMsgIndex == -1 {
		return msgs
	}

	anchor := latestCompleteAssistantAnchor(msgs[:summaryMsgIndex])
	result := make([]message.Message, 0, len(anchor)+len(msgs)-summaryMsgIndex)
	result = append(result, anchor...)
	result = append(result, msgs[summaryMsgIndex:]...)
	// The summary renders as user-role context after the anchor group.
	result[len(anchor)].Role = message.User
	return result
}

// latestCompleteAssistantAnchor walks the compacted prefix backwards and
// returns the latest complete valid assistant turn group. A group spans
// the assistant message and every tool-result message that follows it up
// to the next assistant message. It is complete when every tool call in
// the assistant message has a corresponding result inside the group; an
// incomplete group (a run cancelled between a tool call and its result)
// is skipped and an earlier complete turn is tried. An assistant turn
// without tool calls is complete on its own. No anchor is returned when
// no complete turn exists.
func latestCompleteAssistantAnchor(prefix []message.Message) []message.Message {
	for start := len(prefix) - 1; start >= 0; start-- {
		m := prefix[start]
		if m.Role != message.Assistant || m.IsSummaryMessage || !assistantAnchorViable(m) {
			continue
		}
		calls := m.ToolCalls()
		if len(calls) == 0 {
			// A plain assistant turn is its own complete group. Tool
			// results that follow belong to no call of this turn and
			// are not pulled in.
			return []message.Message{m.Clone()}
		}
		group := []message.Message{m.Clone()}
		results := make(map[string]struct{}, len(calls))
		end := start + 1
		for end < len(prefix) && prefix[end].Role == message.Tool {
			group = append(group, prefix[end].Clone())
			for _, tr := range prefix[end].ToolResults() {
				results[tr.ToolCallID] = struct{}{}
			}
			end++
		}
		complete := true
		for _, call := range calls {
			if _, ok := results[call.ID]; !ok {
				complete = false
				break
			}
		}
		if complete {
			return group
		}
		// Incomplete group: walk back to an earlier complete turn.
	}
	return nil
}

// assistantAnchorViable reports whether an assistant message can anchor
// the replay: it must carry visible content, reasoning parts or tool
// calls. A cancelled assistant with no parts is not a valid anchor.
func assistantAnchorViable(m message.Message) bool {
	if len(m.Parts) == 0 {
		return false
	}
	return m.Content().Text != "" || len(m.ReasoningContents()) > 0 || len(m.ToolCalls()) > 0
}

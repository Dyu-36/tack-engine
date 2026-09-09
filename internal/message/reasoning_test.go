package message

import (
	"encoding/json"
	"testing"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openai"
	"github.com/stretchr/testify/require"
)

func TestPerIDReasoningPartsPreserved(t *testing.T) {
	m := &Message{Role: Assistant}

	m.AppendReasoningContentForID("evt-1", "first ")
	m.AppendReasoningContentForID("evt-2", "second ")
	m.AppendReasoningContentForID("evt-1", "item")

	contents := m.ReasoningContents()
	require.Len(t, contents, 2, "two reasoning items must stay separate")
	require.Equal(t, "evt-1", contents[0].EventID)
	require.Equal(t, "first item", contents[0].Thinking)
	require.Equal(t, "evt-2", contents[1].EventID)
	require.Equal(t, "second ", contents[1].Thinking)

	// Set/finish operate on the matching part only.
	m.SetReasoningResponsesDataForID("evt-2", &openai.ResponsesReasoningMetadata{
		ItemID:           "rs_2",
		EncryptedContent: nil,
		Summary:          []string{"summary"},
	})
	m.FinishThinkingForID("evt-1")

	contents = m.ReasoningContents()
	require.Empty(t, contents[0].ResponsesData, "evt-1 untouched by evt-2 metadata set")
	require.NotZero(t, contents[0].FinishedAt)
	require.Zero(t, contents[1].FinishedAt)
	require.NotNil(t, contents[1].ResponsesData)
	require.Equal(t, "rs_2", contents[1].ResponsesData.ItemID)
	require.Equal(t, []string{"summary"}, contents[1].ResponsesData.Summary)
}

func TestMultiItemReasoningJSONRoundTrip(t *testing.T) {
	encrypted := "enc-secret"
	m := &Message{Role: Assistant}
	m.AppendReasoningContentForID("evt-1", "visible thought")
	m.SetReasoningResponsesDataForID("evt-1", &openai.ResponsesReasoningMetadata{
		ItemID:           "rs_1",
		EncryptedContent: &encrypted,
	})
	m.AppendReasoningContentForID("evt-2", "")
	m.SetReasoningResponsesDataForID("evt-2", &openai.ResponsesReasoningMetadata{
		ItemID: "rs_2",
	})

	raw, err := marshalParts(m.Parts)
	require.NoError(t, err)

	restored, err := unmarshalParts(raw)
	require.NoError(t, err)

	contents := make([]ReasoningContent, 0, len(restored))
	for _, part := range restored {
		if rc, ok := part.(ReasoningContent); ok {
			contents = append(contents, rc)
		}
	}
	require.Len(t, contents, 2, "multi-item reasoning must not collapse through JSON")
	require.Equal(t, "rs_1", contents[0].ResponsesData.ItemID)
	require.NotNil(t, contents[0].ResponsesData.EncryptedContent)
	require.Equal(t, encrypted, *contents[0].ResponsesData.EncryptedContent)
	require.Equal(t, "visible thought", contents[0].Thinking)
	require.Equal(t, "rs_2", contents[1].ResponsesData.ItemID)
}

func TestLegacyResponsesDataNestedFormatUnmarshals(t *testing.T) {
	raw := []byte(`{"type":"reasoning","data":{"type":"reasoning","thinking":"old row","responses_data":{"data":{"item_id":"rs_legacy","summary":[]}}}}`)
	var wrapper struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &wrapper))

	var part ReasoningContent
	require.NoError(t, json.Unmarshal(wrapper.Data, &part))
	require.NotNil(t, part.ResponsesData, "legacy nested data format must still parse")
	require.Equal(t, "rs_legacy", part.ResponsesData.ItemID)
	require.Equal(t, "old row", part.Thinking)
}

func TestToAIMessagePreservesEncryptedOnlyReasoning(t *testing.T) {
	encrypted := "opaque-ciphertext"
	m := &Message{Role: Assistant}
	m.AppendReasoningContentForID("evt-1", "")
	m.SetReasoningResponsesDataForID("evt-1", &openai.ResponsesReasoningMetadata{
		ItemID:           "rs_1",
		EncryptedContent: &encrypted,
	})

	aiMsgs := m.ToAIMessage()
	require.Len(t, aiMsgs, 1)

	var reasoningParts []fantasy.ReasoningPart
	for _, part := range aiMsgs[0].Content {
		if rp, ok := part.(fantasy.ReasoningPart); ok {
			reasoningParts = append(reasoningParts, rp)
		}
	}
	require.Len(t, reasoningParts, 1, "encrypted-only reasoning must not be dropped")

	meta, ok := reasoningParts[0].ProviderOptions[openai.Name].(*openai.ResponsesReasoningMetadata)
	require.True(t, ok)
	require.Equal(t, "rs_1", meta.ItemID)
	require.NotNil(t, meta.EncryptedContent)
	require.Equal(t, encrypted, *meta.EncryptedContent)
	require.Empty(t, reasoningParts[0].Text, "thinking text is not replayed")
}

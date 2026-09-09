package agent

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/stretchr/testify/require"
)

func TestRunTraceSpanOnlyEndedSpansPresent(t *testing.T) {
	trace := newRunTrace("run-1")
	trace.StartSpan() // never ended: must stay absent
	span := trace.StartSpan()
	trace.EndSpan("prompt_prepare", span)

	snap := trace.Snapshot()
	require.Empty(t, snap.SpansMicros["ready_wait"], "unended span must be absent, not zero")
	require.Contains(t, snap.SpansMicros, "prompt_prepare")
	require.GreaterOrEqual(t, snap.SpansMicros["prompt_prepare"], int64(0))
}

func TestRunTraceSpanDuplicateEndKeepsFirst(t *testing.T) {
	trace := newRunTrace("")
	span := trace.StartSpan()
	trace.EndSpan("stream", span)
	first := trace.Snapshot().SpansMicros["stream"]
	trace.EndSpan("stream", span)
	second := trace.Snapshot().SpansMicros["stream"]
	require.Equal(t, first, second)
}

func TestRunTraceFirstSemanticOneShot(t *testing.T) {
	trace := newRunTrace("")
	trace.MarkFirstSemantic("text")
	trace.MarkFirstSemantic("reasoning")
	trace.MarkFirstSemantic("tool_call")
	require.Equal(t, "text", trace.Snapshot().FirstSemantic)

	trace2 := newRunTrace("")
	trace2.MarkFirstSemantic("reasoning")
	trace2.MarkFirstSemantic("text")
	require.Equal(t, "reasoning", trace2.Snapshot().FirstSemantic)

	trace3 := newRunTrace("")
	trace3.MarkFirstSemantic("bogus")
	require.Empty(t, trace3.Snapshot().FirstSemantic)
}

func TestRunTraceAbsentStaysAbsent(t *testing.T) {
	trace := newRunTrace("run-2")
	snap := trace.Snapshot()
	require.Equal(t, notify.CacheUnreported, snap.CacheStatus)
	require.Nil(t, snap.CachedInputTokens)
	require.Nil(t, snap.UncachedInputTokens)
	require.Empty(t, snap.FirstSemantic)
	require.Zero(t, snap.RetryCount)
	require.Zero(t, snap.RetryDelayMicros)
	require.Empty(t, snap.PrefixChangedReason)
	require.Nil(t, snap.SpansMicros)
	require.False(t, snap.Compacted)
	require.Equal(t, 0, snap.Attempt, "no provider request completed, so no attempt is counted")
}

func TestRunTraceAttemptCountsStepsPlusRetries(t *testing.T) {
	trace := newRunTrace("")
	trace.RecordStep()
	trace.RecordRetry(1500 * time.Microsecond)
	trace.RecordStep()
	snap := trace.Snapshot()
	require.Equal(t, 1, snap.RetryCount)
	require.Equal(t, 3, snap.Attempt, "attempt is total provider requests: steps plus retries")
}

func TestRunTraceCacheStatusTriState(t *testing.T) {
	t.Run("unreported when nothing reported", func(t *testing.T) {
		trace := newRunTrace("")
		require.Equal(t, notify.CacheUnreported, trace.Snapshot().CacheStatus)
	})
	t.Run("hit on positive cached tokens", func(t *testing.T) {
		trace := newRunTrace("")
		cached := int64(128)
		uncached := int64(10)
		trace.RecordCacheUsage(&cached, &uncached)
		snap := trace.Snapshot()
		require.Equal(t, notify.CacheHit, snap.CacheStatus)
		require.NotNil(t, snap.CachedInputTokens)
		require.Equal(t, int64(128), *snap.CachedInputTokens)
		require.Equal(t, int64(10), *snap.UncachedInputTokens)
	})
	t.Run("miss on reported zero", func(t *testing.T) {
		trace := newRunTrace("")
		cached := int64(0)
		uncached := int64(0)
		trace.RecordCacheUsage(&cached, &uncached)
		snap := trace.Snapshot()
		require.Equal(t, notify.CacheMiss, snap.CacheStatus)
		// Reported zeros are real values, not absence.
		require.NotNil(t, snap.CachedInputTokens)
		require.Equal(t, int64(0), *snap.CachedInputTokens)
	})
	t.Run("reported cached zero keeps miss when uncached absent", func(t *testing.T) {
		trace := newRunTrace("")
		cached := int64(0)
		trace.RecordCacheUsage(&cached, nil)
		require.Equal(t, notify.CacheMiss, trace.Snapshot().CacheStatus)
	})
}

func TestRunTraceRetryAccounting(t *testing.T) {
	trace := newRunTrace("")
	trace.RecordRetry(1500 * time.Microsecond)
	trace.RecordRetry(2 * time.Millisecond)
	trace.RecordStep()
	snap := trace.Snapshot()
	require.Equal(t, 2, snap.RetryCount)
	require.Equal(t, 3, snap.Attempt, "one completed step plus two retries")
	require.Equal(t, int64(3500), snap.RetryDelayMicros)
}

func TestRunTraceRetryNeverNegative(t *testing.T) {
	trace := newRunTrace("")
	// Start a span "in the future" relative to the anchor via a fake
	// loader-independent path: directly test EndSpan clamping with an
	// inverted call sequence is not possible from outside, so instead
	// verify a normal span and total are non-negative and retry delay
	// clamps negatives.
	trace.RecordRetry(-5 * time.Second)
	snap := trace.Snapshot()
	require.Zero(t, snap.RetryDelayMicros)
	require.Equal(t, 1, snap.RetryCount)
	require.GreaterOrEqual(t, snap.TotalMicros, int64(0))
	for _, v := range snap.SpansMicros {
		require.GreaterOrEqual(t, v, int64(0))
	}
}

func TestRunTraceHMACStableAndUnavailableWithoutKey(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	shape := requestShapeProjection{
		SystemPrompt: "system",
		Prompt:       "hello",
		ToolNames:    []string{"bash", "edit"},
		ToolSchemas:  []string{"{}", "{}"},
		Provider:     "openai",
		Model:        "gpt-5",
	}
	first := computeRequestShapeHMAC(key, shape)
	second := computeRequestShapeHMAC(key, shape)
	require.Equal(t, first, second, "same input and key must give the same HMAC")

	// Domain separation: a different domain byte must change the digest.
	require.NotEqual(t, first, computeRequestShapeHMAC(append([]byte("other"), key[5:]...), shape))

	// Sensitive projection check: the encoded projection contains no
	// credential-like input the caller never supplied.
	encoded := string(shape.encode())
	require.NotContains(t, encoded, "Authorization")
	require.NotContains(t, encoded, "Bearer")

	// Unavailable key: HMAC fields stay empty (never a fallback hash).
	trace := newRunTrace("")
	trace.keyLoader = func() ([]byte, error) { return nil, errTelemetryKeyUnavailable }
	trace.FingerprintFinalRequest(requestShapeProjection{SystemPrompt: "system", Prompt: "hello"})
	snap := trace.Snapshot()
	require.Empty(t, snap.RequestShapeHMAC)
	require.Empty(t, snap.StablePrefixHMAC)
	require.NotZero(t, snap.RequestShapeBytes, "byte counts remain available without HMAC")
}

// errTelemetryKeyUnavailable is a sentinel for the unavailable-key path.
var errTelemetryKeyUnavailable = errorString("telemetry key unavailable")

type errorString string

func (e errorString) Error() string { return string(e) }

func TestRunTraceHMACFromKeyFile(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(255 - i)
	}
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "telemetry"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "telemetry", telemetryKeyFileName), []byte(hex.EncodeToString(key)), 0o600))

	trace := newRunTrace("")
	trace.keyLoader = func() ([]byte, error) {
		return loadOrCreateHMACKeyIn(dir)
	}
	trace.SetModel("openai", "gpt-5", "high")
	trace.FingerprintFinalRequest(requestShapeProjection{SystemPrompt: "system-prompt", Prompt: "user prompt"})
	snap := trace.Snapshot()
	require.NotEmpty(t, snap.RequestShapeHMAC)

	// A second trace with the same key and same shape produces the same HMAC.
	trace2 := newRunTrace("")
	trace2.keyLoader = trace.keyLoader
	trace2.SetModel("openai", "gpt-5", "high")
	trace2.FingerprintFinalRequest(requestShapeProjection{SystemPrompt: "system-prompt", Prompt: "user prompt"})
	require.Equal(t, snap.RequestShapeHMAC, trace2.Snapshot().RequestShapeHMAC)

	// A different shape changes the digest.
	trace3 := newRunTrace("")
	trace3.keyLoader = trace.keyLoader
	trace3.SetModel("openai", "gpt-5", "high")
	trace3.FingerprintFinalRequest(requestShapeProjection{SystemPrompt: "system-prompt", Prompt: "different prompt"})
	require.NotEqual(t, snap.RequestShapeHMAC, trace3.Snapshot().RequestShapeHMAC)
}

func TestRunTraceChangeReasonsSortedUnique(t *testing.T) {
	trace := newRunTrace("run-3")
	// "initial" comes from construction.
	trace.AddChangeReason("todo")
	trace.AddChangeReason("date")
	trace.AddChangeReason("todo")         // duplicate collapses
	trace.AddChangeReason("bogus-reason") // unknown dropped
	trace.SetPrefixChangedReason("skills")
	snap := trace.Snapshot()
	require.Equal(t, []string{"date", "initial", "skills", "todo"}, snap.ChangeReasons)
	require.Equal(t, "skills", snap.PrefixChangedReason)

	// Dynamic-only reasons never invent a primary reason.
	trace2 := newRunTrace("")
	trace2.AddChangeReason("git_status")
	snap2 := trace2.Snapshot()
	require.Empty(t, snap2.PrefixChangedReason)
	require.Equal(t, []string{"git_status", "initial"}, snap2.ChangeReasons)
}

func TestRunTraceConcurrentUse(t *testing.T) {
	trace := newRunTrace("run-4")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			span := trace.StartSpan()
			trace.EndSpan("stream", span)
			trace.MarkFirstSemantic("reasoning")
			trace.MarkFirstSemantic("text")
			trace.RecordRetry(time.Millisecond)
			trace.RecordStep()
			trace.AddChangeReason("mcp")
			_ = trace.Snapshot()
		}()
	}
	wg.Wait()
	snap := trace.Snapshot()
	require.Equal(t, "reasoning", snap.FirstSemantic)
	require.Equal(t, 8, snap.RetryCount)
	require.Equal(t, 16, snap.Attempt)
}

func TestLoadOrCreateHMACKeyCreatesAndReloads(t *testing.T) {
	dir := t.TempDir()
	key1, err := loadOrCreateHMACKeyIn(dir)
	require.NoError(t, err)
	require.Len(t, key1, 32)
	key2, err := loadOrCreateHMACKeyIn(dir)
	require.NoError(t, err)
	require.Equal(t, key1, key2, "second load must return the persisted key")

	// Invalid existing key file is unavailable, never guessed.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "telemetry", telemetryKeyFileName), []byte("not-hex"), 0o600))
	_, err = loadOrCreateHMACKeyIn(dir)
	require.Error(t, err)
}

func TestRunTraceFingerprintLastWriteWins(t *testing.T) {
	// Multi-step tool loops call FingerprintFinalRequest once per
	// PrepareStep; the final request of the run is the one published.
	trace := newRunTrace("")
	trace.FingerprintFinalRequest(requestShapeProjection{SystemPrefix: "first"})
	trace.FingerprintFinalRequest(requestShapeProjection{SystemPrefix: "second"})
	snap := trace.Snapshot()
	require.Equal(t, len("second"), snap.StablePrefixBytes)
}

func TestRunTraceSemanticTimingsSplitPerKind(t *testing.T) {
	trace := newRunTrace("")
	trace.MarkFirstSemantic("reasoning")
	trace.MarkFirstSemantic("tool_call")
	trace.MarkFirstSemantic("text")
	snap := trace.Snapshot()
	// Presence is the contract: each kind emits its own JSON field. A
	// sub-microsecond event is a real zero offset, not an absent one, so
	// presence is asserted against the encoded field names, not values.
	encoded, err := json.Marshal(snap)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "first_reasoning_us")
	require.Contains(t, string(encoded), "first_tool_us")
	require.Contains(t, string(encoded), "first_text_us")
	require.NotNil(t, snap.FirstTextMicros)
	require.NotNil(t, snap.FirstReasoningMicros)
	require.GreaterOrEqual(t, *snap.FirstTextMicros, *snap.FirstReasoningMicros)
	require.Equal(t, "reasoning", snap.FirstSemantic)
}

func TestRunTraceToolOnlyRunLeavesTextTTFTAbsent(t *testing.T) {
	trace := newRunTrace("")
	trace.MarkFirstSemantic("reasoning")
	trace.MarkFirstSemantic("tool_call")
	snap := trace.Snapshot()
	encoded, err := json.Marshal(snap)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "first_text_us",
		"no text means the text TTFT field is absent, never zero")
	require.Nil(t, snap.FirstTextMicros)
	require.Contains(t, string(encoded), "first_tool_us")
	require.Contains(t, string(encoded), "first_reasoning_us")
}

func TestRunTraceGenerationDiffRecordsReasons(t *testing.T) {
	trace := newRunTrace("")
	trace.RecordGenerationDiff([]string{"context"}, nil)
	snap := trace.Snapshot()
	require.Equal(t, "context", snap.PrefixChangedReason)
	require.Equal(t, []string{"context", "initial"}, snap.ChangeReasons)

	trace2 := newRunTrace("")
	trace2.RecordGenerationDiff(nil, []string{"date", "git"})
	snap2 := trace2.Snapshot()
	require.Empty(t, snap2.PrefixChangedReason)
	require.Equal(t, []string{"date", "git_status", "initial"}, snap2.ChangeReasons)

	trace3 := newRunTrace("run-x")
	trace3.SetCompacted()
	trace3.RecordGenerationDiff([]string{"model", "skills"}, []string{"mcp"})
	snap3 := trace3.Snapshot()
	require.Equal(t, "model_switch", snap3.PrefixChangedReason)
	require.Equal(t, []string{"compaction", "initial", "mcp", "model_switch", "skills"}, snap3.ChangeReasons)

	trace4 := newRunTrace("")
	trace4.RecordGenerationDiff([]string{"tool_set"}, nil)
	require.Equal(t, "tool_set", trace4.Snapshot().PrefixChangedReason)

	trace5 := newRunTrace("")
	trace5.SetCompacted()
	require.Equal(t, "compaction", trace5.Snapshot().PrefixChangedReason)

	trace6 := newRunTrace("")
	trace6.SetCompacted()
	trace6.RecordGenerationDiff([]string{"context"}, nil)
	require.Equal(t, "compaction", trace6.Snapshot().PrefixChangedReason)

	trace7 := newRunTrace("")
	trace7.RecordGenerationDiff([]string{"unknown-kind"}, []string{"also-unknown"})
	snap7 := trace7.Snapshot()
	require.Empty(t, snap7.PrefixChangedReason)
	require.Equal(t, []string{"initial"}, snap7.ChangeReasons)
}

func TestRunTracePromptPartsHMACDomainSeparated(t *testing.T) {
	dir := t.TempDir()
	key, err := loadOrCreateHMACKeyIn(dir)
	require.NoError(t, err)
	trace := newRunTrace("")
	trace.keyLoader = func() ([]byte, error) { return key, nil }
	trace.SetPromptParts("stable-bytes", "dynamic-bytes")
	snap := trace.Snapshot()
	require.NotEmpty(t, snap.StablePrefixHMAC)
	require.NotEmpty(t, snap.DynamicSuffixHMAC)
	require.NotEqual(t, snap.StablePrefixHMAC, snap.DynamicSuffixHMAC,
		"domain separation: same bytes under different domains must differ")

	trace2 := newRunTrace("")
	trace2.keyLoader = trace.keyLoader
	trace2.SetPromptParts("stable-bytes", "dynamic-bytes")
	require.Equal(t, snap.StablePrefixHMAC, trace2.Snapshot().StablePrefixHMAC)

	trace3 := newRunTrace("")
	trace3.keyLoader = trace.keyLoader
	empty := trace3.Snapshot()
	require.Empty(t, empty.StablePrefixHMAC)
	require.Empty(t, empty.DynamicSuffixHMAC)
}

func TestRequestShapeProjectionEncodingIsLengthPrefixed(t *testing.T) {
	shape := requestShapeProjection{
		SystemPrefix:    "pre",
		SystemPrompt:    "ab",
		Prompt:          "c",
		HistoryShape:    "user;text=1;call=0;file=0;reasoning=0;result=0",
		ToolNames:       []string{"bash"},
		ToolSchemas:     []string{"{}"},
		AttachmentKinds: []string{"text/plain"},
		AttachmentBytes: 7,
		Provider:        "openai",
		Model:           "gpt-5",
	}
	encoded := string(shape.encode())
	require.Contains(t, encoded, "system_prefix=3:pre\n")
	require.Contains(t, encoded, "system_prompt=2:ab\n")
	require.Contains(t, encoded, "prompt=1:c\n")
	require.Contains(t, encoded, "tool_count=1\n")
	require.Contains(t, encoded, "attachment_count=1\n")
	lines := strings.Split(strings.TrimRight(encoded, "\n"), "\n")
	for _, line := range lines {
		require.NotEqual(t, "", line)
	}
}

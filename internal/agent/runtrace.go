package agent

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/agent/notify"
)

// requestShapeHMACDomain is the domain-separation prefix for the
// request-shape HMAC so digests computed for other purposes are never
// interchangeable with it.
const requestShapeHMACDomain = "gotack.request-shape.v1"

// telemetryKeyFileName is the 32-byte HMAC key file kept under the
// engine data directory. It is generated once per installation and never
// leaves the machine.
const telemetryKeyFileName = "telemetry-hmac.key"

// changeReasonAllowlist is the closed enum for telemetry change reasons.
// Primary reasons follow the fixed precedence (model_switch > compaction
// > context > skills > tool_set > none); dynamic-only reasons (date,
// git_status, mcp, todo, provider_options) never move the primary
// reason, and "initial" marks the first observation of a run.
var changeReasonAllowlist = map[string]struct{}{
	"git_status":       {},
	"date":             {},
	"mcp":              {},
	"skills":           {},
	"context":          {},
	"tool_set":         {},
	"compaction":       {},
	"model_switch":     {},
	"none":             {},
	"initial":          {},
	"todo":             {},
	"provider_options": {},
}

// runTraceKeyDirPath is the engine data directory used to resolve the
// telemetry HMAC key. The coordinator sets it once at startup so traces
// created on the sessionAgent side (which has no config access) resolve
// the same key.
var runTraceKeyDirPath string

// SetRunTraceKeyDir points telemetry HMAC key resolution at the engine
// data directory. Passing an empty string marks the key unavailable.
func SetRunTraceKeyDir(dir string) {
	runTraceKeyDirPath = dir
}

// requestShapeProjection is the sanitized projection of the FINAL
// prepared model request that feeds the request-shape HMAC. It is
// captured at the end of PrepareStep — after prompt/history preparation,
// the todo reminder, queued-prompt folding, provider media workarounds,
// cache-control options and the tool set are all final — so the digest
// describes the wire-bound request, not an intermediate one.
//
// It deliberately excludes provider credentials, encrypted reasoning
// ciphertext, tool-result bodies (raw tool output) and session UUIDs or
// message IDs. Only field labels, counts, byte lengths and the engine
// owned prompt text enter the digest input; the projection itself is
// never logged, only its HMAC and byte counts are published.
type requestShapeProjection struct {
	SystemPrefix    string
	SystemPrompt    string
	Prompt          string
	HistoryShape    string
	ToolNames       []string
	ToolSchemas     []string
	AttachmentKinds []string
	AttachmentBytes int64
	Provider        string
	Model           string
	ReasoningEffort string
}

func (p requestShapeProjection) encode() []byte {
	var b strings.Builder
	writeField := func(label, value string) {
		b.WriteString(label)
		b.WriteByte('=')
		b.WriteString(fmt.Sprint(len(value)))
		b.WriteByte(':')
		b.WriteString(value)
		b.WriteByte('\n')
	}
	writeField("system_prefix", p.SystemPrefix)
	writeField("system_prompt", p.SystemPrompt)
	writeField("prompt", p.Prompt)
	writeField("history_shape", p.HistoryShape)
	b.WriteString(fmt.Sprintf("tool_count=%d\n", len(p.ToolNames)))
	b.WriteString("tool_names=")
	b.WriteString(strings.Join(p.ToolNames, ","))
	b.WriteByte('\n')
	for i, schema := range p.ToolSchemas {
		b.WriteString(fmt.Sprintf("tool_schema[%d]=%d:%s\n", i, len(schema), schema))
	}
	b.WriteString(fmt.Sprintf("attachment_count=%d\n", len(p.AttachmentKinds)))
	for i, kind := range p.AttachmentKinds {
		b.WriteString(fmt.Sprintf("attachment_kind[%d]=%s\n", i, kind))
	}
	b.WriteString(fmt.Sprintf("attachment_bytes=%d\n", p.AttachmentBytes))
	b.WriteString(fmt.Sprintf("provider=%s\n", p.Provider))
	b.WriteString(fmt.Sprintf("model=%s\n", p.Model))
	b.WriteString(fmt.Sprintf("reasoning_effort=%s\n", p.ReasoningEffort))
	return []byte(b.String())
}

// RunTrace collects monotonic timing, retry accounting and request-shape
// telemetry for a single agent run. It is safe for concurrent use: stream
// callbacks, the coordinator and PrepareStep all touch it from different
// goroutines.
//
// Rules (ImplementPlan 0.2 / PR0):
//   - One time.Now() anchor per run; durations come from time.Since so
//     they are monotonic and never negative.
//   - An event that never happened is absent, never zero: the spans map
//     only contains spans whose EndSpan actually ran.
//   - first_semantic is one-shot: the first observed semantic kind wins
//     and later marks never overwrite it.
//   - Cache status is tri-state and defaults to "unreported"; it only
//     flips to hit/miss from actually reported cache token fields, and
//     the token counts stay nil pointers when absent.
//   - When the HMAC key is unavailable the HMAC fields stay empty; they
//     never fall back to a plaintext or unsalted hash.
type RunTrace struct {
	mu sync.Mutex

	runID  string
	anchor time.Time

	// endedMicros maps span name to its duration in microseconds. It is
	// only written by EndSpan, so spans that never ended stay absent.
	endedMicros map[string]int64

	firstSemantic string
	// first<Kind>Seen distinguishes "never happened" from "happened
	// within the first microsecond": the micros value alone cannot.
	firstReasoningSeen  bool
	firstReasoningMicros int64
	firstToolSeen        bool
	firstToolMicros      int64
	firstTextSeen        bool
	firstTextMicros      int64
	retryCount    int
	retryDelay    time.Duration

	provider        string
	model           string
	reasoningEffort string

	cacheStatus         notify.CacheStatus
	cachedInputTokens   int64
	uncachedInputTokens int64
	usageReported       bool
	stepCount           int

	compacted bool

	// promptStable/promptDynamic hold the run's actual system prompt
	// split (dynamic includes the MCP instruction block) for the per-run
	// stable_prefix_hmac and dynamic_suffix_hmac.
	promptStable  string
	promptDynamic string

	// changeReasons accumulates the set of reasons observed in this run;
	// it is emitted sorted and deduplicated.
	changeReasons map[string]struct{}

	fingerprinted bool
	prefixChanged string
	shape         requestShapeProjection

	// hmacKey is the cached 32-byte key; nil means "not loaded yet" or
	// "load failed". keyLoaded distinguishes the two so a failed load is
	// not retried on every snapshot.
	hmacKey   []byte
	keyLoaded bool
	// keyLoader is a test seam; nil selects the real file-backed loader.
	keyLoader func() ([]byte, error)
}

// spanStart is the handle returned by StartSpan and consumed by EndSpan.
// The zero value ends nothing, so a failed StartSpan can never fabricate
// a duration.
type spanStart struct {
	start time.Time
	valid bool
}

// newRunTrace starts a run trace anchored at the current monotonic clock.
// The first observation of a run is recorded as the "initial" change
// reason.
func newRunTrace(runID string) *RunTrace {
	return &RunTrace{
		runID:         runID,
		anchor:        time.Now(),
		endedMicros:   make(map[string]int64),
		cacheStatus:   notify.CacheUnreported,
		changeReasons: map[string]struct{}{"initial": {}},
	}
}

// StartSpan marks the start of a span. The name is supplied at EndSpan,
// so spans started before their outcome is known need no name here.
func (t *RunTrace) StartSpan() spanStart {
	if t == nil {
		return spanStart{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.startSpanLocked(time.Now())
}

func (t *RunTrace) startSpanLocked(now time.Time) spanStart {
	if !now.After(t.anchor) {
		// Clamp to the anchor so clock skew or injected clocks can never
		// produce a negative offset.
		now = t.anchor
	}
	return spanStart{start: now, valid: true}
}

// EndSpan records the elapsed duration of a span in microseconds. Only
// ended spans appear in the snapshot; a span that never ended is absent,
// and ending the same span name twice keeps the first measurement.
func (t *RunTrace) EndSpan(name string, start spanStart) {
	if t == nil || !start.valid {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, done := t.endedMicros[name]; done {
		return
	}
	offset := time.Since(start.start)
	if offset < 0 {
		offset = 0
	}
	t.endedMicros[name] = offset.Microseconds()
}

// MarkFirstSemantic records the first semantic output kind observed
// ("reasoning", "tool_call" or "text"). It is one-shot: later marks never
// overwrite the first observation, and unknown kinds are ignored. Each
// kind also records its own one-shot monotonic offset so text TTFT is
// never hidden behind an earlier reasoning or tool mark (ImplementPlan
// 0.2): a tool-only run leaves the text offset absent, never zero.
func (t *RunTrace) MarkFirstSemantic(kind string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var seen *bool
	var target *int64
	switch kind {
	case "reasoning":
		seen, target = &t.firstReasoningSeen, &t.firstReasoningMicros
	case "tool_call":
		seen, target = &t.firstToolSeen, &t.firstToolMicros
	case "text":
		seen, target = &t.firstTextSeen, &t.firstTextMicros
	default:
		return
	}
	if t.firstSemantic == "" {
		t.firstSemantic = kind
	}
	if *seen {
		return
	}
	*seen = true
	offset := time.Since(t.anchor).Microseconds()
	if offset < 0 {
		offset = 0
	}
	*target = offset
}

// RecordRetry accounts for one provider retry: it increments the retry
// counter and accumulates the backoff delay.
func (t *RunTrace) RecordRetry(delay time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if delay < 0 {
		delay = 0
	}
	t.retryCount++
	t.retryDelay += delay
}

// SetModel records the provider/model identifiers (and effective
// reasoning effort) used by this run. These are configuration
// identifiers, not secrets.
func (t *RunTrace) SetModel(provider, model, reasoningEffort string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.provider = provider
	t.model = model
	t.reasoningEffort = reasoningEffort
}

// SetCompacted marks the run as having ended on an auto-summarize
// (compaction) boundary. Compaction participates in the primary reason
// precedence (below model_switch, above context): it becomes the primary
// reason unless a higher-precedence stable reason already claimed it.
func (t *RunTrace) SetCompacted() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.compacted = true
	if t.changeReasons == nil {
		t.changeReasons = make(map[string]struct{})
	}
	t.changeReasons["compaction"] = struct{}{}
	if t.prefixChanged == "" {
		t.prefixChanged = "compaction"
	}
}

// SetPrefixChangedReason records the primary prefix-changed reason using
// the fixed reason enum. The reason is supplied by the caller from a
// generation diff, never inferred from a hash. Empty means absent. The
// value also joins the ChangeReasons set.
func (t *RunTrace) SetPrefixChangedReason(reason string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prefixChanged = reason
	if reason == "" {
		return
	}
	if _, allowed := changeReasonAllowlist[reason]; !allowed {
		return
	}
	if t.changeReasons == nil {
		t.changeReasons = make(map[string]struct{})
	}
	t.changeReasons[reason] = struct{}{}
}

// AddChangeReason records a dynamic-only (non-primary) change observed
// during the run. Unknown reasons are dropped so telemetry consumers can
// rely on the closed enum. Duplicates collapse; Snapshot emits the set
// sorted ascending.
func (t *RunTrace) AddChangeReason(reason string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, allowed := changeReasonAllowlist[reason]; !allowed {
		return
	}
	if t.changeReasons == nil {
		t.changeReasons = make(map[string]struct{})
	}
	t.changeReasons[reason] = struct{}{}
}

// RecordCacheUsage accumulates provider-reported token usage for the run.
// A nil field means the provider did not report it for that request; a
// non-nil zero is a reported zero. Sums across steps because the telemetry
// is run-scoped. The status only moves off "unreported" when at least one
// cache field was actually reported, and reported zeros preserve "miss"
// instead of erasing it.
func (t *RunTrace) RecordCacheUsage(cached, uncached *int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if cached != nil {
		t.cachedInputTokens += *cached
		t.usageReported = true
	}
	if uncached != nil {
		t.uncachedInputTokens += *uncached
		t.usageReported = true
	}
	switch {
	case t.cachedInputTokens > 0:
		t.cacheStatus = notify.CacheHit
	case t.usageReported:
		t.cacheStatus = notify.CacheMiss
	}
}

// SetPromptParts records the stable prefix and dynamic suffix system
// bytes actually used by this run (the dynamic part includes the per-run
// MCP instruction block). Snapshot derives the per-run
// stable_prefix_hmac and dynamic_suffix_hmac from these bytes; without a
// usable HMAC key the digests stay absent.
func (t *RunTrace) SetPromptParts(stable, dynamic string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.promptStable = stable
	t.promptDynamic = dynamic
}

// generationReasonByComponent maps a changed generation component kind
// to its telemetry change reason. Template and policy edits are
// classified as context changes per ImplementPlan 0.2.
var generationReasonByComponent = map[string]string{
	"template": "context",
	"model":    "model_switch",
	"context":  "context",
	"skills":   "skills",
	"tool_set": "tool_set",
	"mcp":      "mcp",
	"date":     "date",
	"git":      "git_status",
}

// stableReasonPrecedence is the fixed primary reason order: model_switch
// > compaction > context > skills > tool_set > none.
var stableReasonPrecedence = []string{"model_switch", "compaction", "context", "skills", "tool_set"}

// RecordGenerationDiff consumes the changed component kinds from a
// prompt generation diff and records the matching change reasons. The
// primary prefix-changed reason is the highest-precedence stable reason;
// a dynamic-only change records its reasons and leaves the primary
// reason empty for this run's diff (compaction sets its own). Reasons
// for unknown component kinds are dropped: the enum is closed.
func (t *RunTrace) RecordGenerationDiff(stableComponents, dynamicComponents []string) {
	if t == nil {
		return
	}
	reasons := make(map[string]struct{}, len(stableComponents)+len(dynamicComponents))
	for _, kind := range stableComponents {
		if reason, ok := generationReasonByComponent[kind]; ok {
			reasons[reason] = struct{}{}
		}
	}
	for _, kind := range dynamicComponents {
		if reason, ok := generationReasonByComponent[kind]; ok {
			reasons[reason] = struct{}{}
		}
	}
	// tool_set is not a generation component; the caller passes it as a
	// stable component kind directly.
	t.mu.Lock()
	defer t.mu.Unlock()
	for reason := range reasons {
		if t.changeReasons == nil {
			t.changeReasons = make(map[string]struct{})
		}
		t.changeReasons[reason] = struct{}{}
		if reason == "model_switch" {
			// Highest precedence; nothing can displace it.
			t.prefixChanged = "model_switch"
			continue
		}
		if _, isStable := stableReasonPrecedenceIndex(reason); !isStable {
			continue
		}
		if t.prefixChanged == "" {
			t.prefixChanged = reason
		}
	}
}

// stableReasonPrecedenceIndex reports whether the reason participates in
// the primary precedence and its rank (lower wins).
func stableReasonPrecedenceIndex(reason string) (int, bool) {
	for i, candidate := range stableReasonPrecedence {
		if candidate == reason {
			return i, true
		}
	}
	return 0, false
}

// RecordStep accounts for one completed provider step. Attempt is the
// number of provider requests issued for the run: completed steps plus
// retries.
func (t *RunTrace) RecordStep() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stepCount++
}

// FingerprintFinalRequest captures the sanitized request-shape
// projection of the FINAL prepared request. Multi-step tool loops call
// it once per PrepareStep and the last call wins, so the published
// fingerprint describes the final wire-bound request of the run. The
// HMAC itself is computed lazily in Snapshot so a missing key simply
// leaves the HMAC fields empty.
func (t *RunTrace) FingerprintFinalRequest(shape requestShapeProjection) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.fingerprinted = true
	t.shape = shape
}

// Snapshot renders the accumulated telemetry. Fields for events that
// never happened stay absent per the schema's omitempty rules; the cache
// status is always present and defaults to "unreported".
func (t *RunTrace) Snapshot() *notify.RunTelemetry {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	telemetry := &notify.RunTelemetry{
		RunID:           t.runID,
		Provider:        t.provider,
		Model:           t.model,
		ReasoningEffort: t.reasoningEffort,
		Attempt:         t.retryCount + t.stepCount,
		RetryCount:      t.retryCount,
		TotalMicros:     time.Since(t.anchor).Microseconds(),
		FirstSemantic:   t.firstSemantic,
		CacheStatus:     t.cacheStatus,
	}
	if telemetry.CacheStatus == "" {
		telemetry.CacheStatus = notify.CacheUnreported
	}
	if t.retryDelay > 0 {
		telemetry.RetryDelayMicros = t.retryDelay.Microseconds()
	}
	if len(t.endedMicros) > 0 {
		spans := make(map[string]int64, len(t.endedMicros))
		for name, micros := range t.endedMicros {
			spans[name] = micros
		}
		telemetry.SpansMicros = spans
	}
	if t.usageReported {
		cached := t.cachedInputTokens
		uncached := t.uncachedInputTokens
		telemetry.CachedInputTokens = &cached
		telemetry.UncachedInputTokens = &uncached
	}
	if t.compacted {
		telemetry.Compacted = true
	}
	if t.prefixChanged != "" {
		telemetry.PrefixChangedReason = t.prefixChanged
	}
	if len(t.changeReasons) > 0 {
		reasons := make([]string, 0, len(t.changeReasons))
		for reason := range t.changeReasons {
			reasons = append(reasons, reason)
		}
		sort.Strings(reasons)
		telemetry.ChangeReasons = reasons
	}
	if t.fingerprinted {
		telemetry.StablePrefixBytes = len(t.shape.SystemPrefix) + len(t.shape.SystemPrompt)
		telemetry.DynamicSuffixBytes = len(t.shape.Prompt)
		telemetry.RequestShapeBytes = len(t.shape.encode())
		if key := t.hmacKeyLocked(); key != nil {
			telemetry.RequestShapeHMAC = computeRequestShapeHMAC(key, t.shape)
		}
		// Without a usable key the HMAC fields stay empty: unavailable
		// never falls back to an unsalted or plaintext hash.
	}
	// The stable/dynamic split digests cover the actual system prompt
	// bytes of the run; both stay absent when the key is unavailable or
	// the split was never recorded.
	if key := t.hmacKeyLocked(); key != nil && (t.promptStable != "" || t.promptDynamic != "") {
		telemetry.StablePrefixHMAC = keyedDigest(key, "gotack.stable-prefix.v1", t.promptStable)
		telemetry.DynamicSuffixHMAC = keyedDigest(key, "gotack.dynamic-suffix.v1", t.promptDynamic)
	}
	// Pointer semantics match the cache-token fields: nil means the
	// event never happened; a non-nil zero is a real sub-microsecond
	// offset. omitempty on a plain int64 would erase a legitimate zero.
	if t.firstReasoningSeen {
		value := t.firstReasoningMicros
		telemetry.FirstReasoningMicros = &value
	}
	if t.firstToolSeen {
		value := t.firstToolMicros
		telemetry.FirstToolMicros = &value
	}
	if t.firstTextSeen {
		value := t.firstTextMicros
		telemetry.FirstTextMicros = &value
	}
	return telemetry
}

// hmacKeyLocked lazily resolves the HMAC key, caching both success and
// failure for the lifetime of the trace.
func (t *RunTrace) hmacKeyLocked() []byte {
	if t.hmacKey != nil {
		return t.hmacKey
	}
	if t.keyLoaded {
		return nil
	}
	t.keyLoaded = true
	loader := t.keyLoader
	if loader == nil {
		loader = loadOrCreateHMACKey
	}
	key, err := loader()
	if err != nil || len(key) != 32 {
		return nil
	}
	t.hmacKey = key
	return t.hmacKey
}

// computeRequestShapeHMAC is a pure function so tests can assert
// domain separation and stability without touching the filesystem.
func computeRequestShapeHMAC(key []byte, shape requestShapeProjection) string {
	return keyedDigest(key, requestShapeHMACDomain, string(shape.encode()))
}

// keyedDigest is the shared domain-separated HMAC used by every
// published telemetry digest.
func keyedDigest(key []byte, domain, value string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(domain))
	mac.Write([]byte{0})
	mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// loadOrCreateHMACKey loads the 32-byte telemetry HMAC key under the
// engine data directory configured via SetRunTraceKeyDir.
func loadOrCreateHMACKey() ([]byte, error) {
	return loadOrCreateHMACKeyIn(runTraceKeyDirPath)
}

// loadOrCreateHMACKeyIn loads the 32-byte telemetry HMAC key from the
// given engine data directory, creating it on first use. Creation goes
// through an exclusive create followed by a verified write, and a reader
// of an empty or unparseable key file treats the key as unavailable
// rather than guessing. Errors are reported as reason codes only; no
// path or system error text is echoed into telemetry.
func loadOrCreateHMACKeyIn(dataDir string) ([]byte, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("telemetry key directory unavailable")
	}
	dir := filepath.Join(dataDir, "telemetry")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("telemetry key directory create failed")
	}
	path := filepath.Join(dir, telemetryKeyFileName)
	if data, err := os.ReadFile(path); err == nil {
		key, parseErr := parseHMACKey(data)
		if parseErr != nil {
			return nil, fmt.Errorf("telemetry key invalid")
		}
		return key, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("telemetry key generation failed")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		// Another writer may have created it between the read and the
		// create; adopt it only when it parses as a valid key.
		if data, readErr := os.ReadFile(path); readErr == nil {
			if other, parseErr := parseHMACKey(data); parseErr == nil {
				return other, nil
			}
		}
		return nil, fmt.Errorf("telemetry key create failed")
	}
	defer f.Close()
	if _, err := f.Write([]byte(hex.EncodeToString(key))); err != nil {
		return nil, fmt.Errorf("telemetry key write failed")
	}
	return key, nil
}

func parseHMACKey(data []byte) ([]byte, error) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil, fmt.Errorf("empty key file")
	}
	key, err := hex.DecodeString(text)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("invalid key file")
	}
	return key, nil
}

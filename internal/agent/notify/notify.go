// Package notify defines domain notification types for agent events.
// These types are decoupled from UI concerns so the agent can publish
// events without importing UI packages.
package notify

// Type identifies the kind of agent notification.
type Type string

const (
	// TypeAgentFinished indicates the agent has completed its turn.
	TypeAgentFinished Type = "agent_finished"
	// TypeReAuthenticate indicates the agent encountered an
	// authentication error and the user needs to re-authenticate.
	TypeReAuthenticate Type = "re_authenticate"
	// TypeAgentError indicates the agent's turn terminated with an
	// error. The error text is carried in Notification.Message.
	TypeAgentError Type = "error"
	// TypeAWSSSOAuth indicates AWS SSO credentials have expired and the
	// coordinator is running the configured refresh command. It opens the
	// AWS SSO dialog; a follow-up with the same type carries the SSO URL
	// once it appears in the command output. AWSSOCommand carries the
	// command being run; AWSSOURL carries the verification URL when known.
	TypeAWSSSOAuth Type = "aws_sso_auth"
	// TypeAWSSSOAuthResult indicates the AWS SSO refresh command has
	// finished. Message carries the error text when it failed, empty on
	// success.
	TypeAWSSSOAuthResult Type = "aws_sso_auth_result"
)

// Notification represents a domain event published by the agent.
type Notification struct {
	SessionID    string
	SessionTitle string
	Type         Type
	ProviderID   string
	// RunID, when non-empty, is the caller-supplied correlator
	// (proto.AgentMessage.RunID) for the run that produced this
	// notification. It lets observers attribute a TypeAgentError to a
	// specific request rather than to any in-flight run on the
	// session. Empty when no caller set one.
	RunID string
	// Message carries the error text for TypeAgentError. Other
	// notification types ignore it.
	Message string
	// AWSSOCommand carries the shell command for TypeAWSSSOAuth.
	AWSSOCommand string
	// AWSSOURL carries the SSO verification URL for TypeAWSSSOAuth once it
	// appears in the refresh command's output.
	AWSSOURL string
}

// RunComplete is the authoritative end-of-run signal for a session.
// It is published exactly once per top-level agent run (per
// [sessionAgent.Run] invocation that actually executed) after all
// message updates for the turn have been flushed via
// message.Service.FlushAll. Carries the final assistant text and
// message ID so non-interactive clients can reconcile stdout even if
// SSE events arrive out of order or are dropped by the broker. Error
// is non-empty when the run terminated with an error; Cancelled is
// true when the run terminated due to context cancellation. The two
// are mutually exclusive in the success case but may overlap when a
// cancel triggers a downstream error.
//
// RunID identifies the specific request that produced this event.
// It is the value the caller set on `proto.AgentMessage.RunID` (or
// equivalently propagated via agent.WithRunID on the context that
// reaches the coordinator); empty when no caller set one. Filtering
// by RunID lets a client correlate a SendMessage call with its
// terminal event even when the session is busy and other turns are
// finishing on the same session.
type RunComplete struct {
	SessionID string
	RunID     string
	MessageID string
	Text      string
	Error     string
	Cancelled bool
	Telemetry *RunTelemetry
}

type CacheStatus string

const (
	CacheHit        CacheStatus = "hit"
	CacheMiss       CacheStatus = "miss"
	CacheUnreported CacheStatus = "unreported"
)

type RunTelemetry struct {
	RunID               string           `json:"run_id,omitempty"`
	Provider            string           `json:"provider,omitempty"`
	Model               string           `json:"model,omitempty"`
	ReasoningEffort     string           `json:"reasoning_effort,omitempty"`
	Attempt             int              `json:"attempt"`
	RetryCount          int              `json:"retry_count"`
	RetryDelayMicros    int64            `json:"retry_delay_us,omitempty"`
	SpansMicros         map[string]int64 `json:"spans_us,omitempty"`
	TotalMicros         int64            `json:"total_us"`
	FirstSemantic       string           `json:"first_semantic,omitempty"`
	// Per-kind one-shot semantic offsets (microseconds since run start).
	// A kind that never happened stays absent, never zero, so a
	// tool-only run does not report a text TTFT of 0.
	FirstReasoningMicros *int64          `json:"first_reasoning_us,omitempty"`
	FirstToolMicros      *int64          `json:"first_tool_us,omitempty"`
	FirstTextMicros      *int64          `json:"first_text_us,omitempty"`
	CacheStatus         CacheStatus      `json:"cache_status"`
	CachedInputTokens   *int64           `json:"cached_input_tokens,omitempty"`
	UncachedInputTokens *int64           `json:"uncached_input_tokens,omitempty"`
	ServiceTier         string           `json:"service_tier,omitempty"`
	ProviderRequestID   string           `json:"provider_request_id,omitempty"`
	EstimatedUsage      bool             `json:"estimated_usage,omitempty"`
	Compacted           bool             `json:"compacted,omitempty"`
	PrefixChangedReason string           `json:"prefix_changed_reason,omitempty"`
	// ChangeReasons is the optional sorted, unique list of every prefix
	// change observed in the run, including dynamic-only reasons that do
	// not move the primary PrefixChangedReason. Allowed values:
	// git_status, date, mcp, skills, context, tool_set, compaction,
	// model_switch, none, initial, todo, provider_options.
	ChangeReasons      []string `json:"change_reasons,omitempty"`
	StablePrefixHMAC   string   `json:"stable_prefix_hmac,omitempty"`
	StablePrefixBytes  int      `json:"stable_prefix_bytes,omitempty"`
	DynamicSuffixHMAC  string   `json:"dynamic_suffix_hmac,omitempty"`
	DynamicSuffixBytes int      `json:"dynamic_suffix_bytes,omitempty"`
	RequestShapeHMAC   string   `json:"request_shape_hmac,omitempty"`
	RequestShapeBytes  int      `json:"request_shape_bytes,omitempty"`
}

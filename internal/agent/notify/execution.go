package notify

type BuildTelemetry struct {
	ID           string `json:"id,omitempty"`
	Commit       string `json:"commit,omitempty"`
	Modified     bool   `json:"modified"`
	SourceDigest string `json:"source_digest,omitempty"`
	BuiltAt      string `json:"built_at,omitempty"`
}

type ReasoningTelemetry struct {
	Effort        string `json:"effort,omitempty"`
	Thinking      string `json:"thinking,omitempty"`
	BudgetTokens  *int64 `json:"budget_tokens,omitempty"`
	ClearThinking *bool  `json:"clear_thinking,omitempty"`
	ToolStream    *bool  `json:"tool_stream,omitempty"`
}

type ModelCallTelemetry struct {
	ID                     int                `json:"id"`
	Step                   int                `json:"step"`
	Purpose                string             `json:"purpose"`
	Provider               string             `json:"provider"`
	Model                  string             `json:"model"`
	StartedMicros          int64              `json:"started_us"`
	EndedMicros            *int64             `json:"ended_us,omitempty"`
	FirstStreamEventMicros *int64             `json:"first_stream_event_us,omitempty"`
	FirstReasoningMicros   *int64             `json:"first_reasoning_us,omitempty"`
	ReasoningEndMicros     *int64             `json:"reasoning_end_us,omitempty"`
	FirstToolMicros        *int64             `json:"first_tool_us,omitempty"`
	ToolInputEndMicros     *int64             `json:"tool_input_end_us,omitempty"`
	FirstTextMicros        *int64             `json:"first_text_us,omitempty"`
	DeliveryMicros         int64              `json:"delivery_us"`
	RequestedEffort        string             `json:"requested_effort,omitempty"`
	RequestedThinking      bool               `json:"requested_thinking"`
	ResolvedEffort         string             `json:"resolved_effort,omitempty"`
	FinalReasoning         ReasoningTelemetry `json:"final_reasoning"`
	MaxOutputTokens        *int64             `json:"max_output_tokens,omitempty"`
	InputTokens            *int64             `json:"input_tokens,omitempty"`
	OutputTokens           *int64             `json:"output_tokens,omitempty"`
	ReasoningTokens        *int64             `json:"reasoning_tokens,omitempty"`
	CacheReadTokens        *int64             `json:"cache_read_tokens,omitempty"`
	Failed                 bool               `json:"failed"`
	Cancelled              bool               `json:"cancelled"`
}

type ToolCallTelemetry struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Step             int    `json:"step"`
	StartedMicros    int64  `json:"started_us"`
	EndedMicros      int64  `json:"ended_us"`
	HookMicros       int64  `json:"hook_us"`
	PermissionMicros int64  `json:"permission_us"`
	IsError          bool   `json:"is_error"`
	Status           string `json:"status,omitempty"`
	ExitCode         *int   `json:"exit_code,omitempty"`
}

type ProviderAttemptTelemetry struct {
	ModelCallID               int    `json:"model_call_id"`
	HTTPAttempt               int    `json:"http_attempt"`
	Purpose                   string `json:"purpose"`
	RequestEncodedMicros      *int64 `json:"request_encoded_us,omitempty"`
	RequestWrittenMicros      *int64 `json:"request_written_us,omitempty"`
	FirstResponseByteMicros   *int64 `json:"first_response_byte_us,omitempty"`
	ResponseHeadersMicros     *int64 `json:"response_headers_us,omitempty"`
	FirstSSEFrameMicros       *int64 `json:"first_sse_frame_us,omitempty"`
	FirstByteToFirstSSEMicros *int64 `json:"first_byte_to_first_sse_us,omitempty"`
}

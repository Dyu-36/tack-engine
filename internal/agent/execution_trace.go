package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptrace"
	"sort"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/runobserve"
)

const maxExecutionRecords = 2048

type executionTraceKey struct{}

func traceModel(ctx context.Context, model Model, purpose string) fantasy.LanguageModel {
	trace, _ := ctx.Value(executionTraceKey{}).(*RunTrace)
	if trace == nil {
		return model.Model
	}
	return tracedModel{LanguageModel: model.Model, selected: model, trace: trace, purpose: purpose}
}

type tracedModel struct {
	fantasy.LanguageModel
	trace    *RunTrace
	selected Model
	purpose  string
}

func (m tracedModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	id := m.trace.beginModelCall(m.selected, m.purpose, call)
	transportAttempt := 0
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) {
			m.trace.mu.Lock()
			defer m.trace.mu.Unlock()
			if id == 0 || len(m.trace.providerAttempts) >= maxExecutionRecords {
				m.trace.executionRecordsDropped++
				return
			}
			transportAttempt++
			m.trace.providerAttempts = append(m.trace.providerAttempts, notify.ProviderAttemptTelemetry{
				ModelCallID: id, HTTPAttempt: transportAttempt, Purpose: m.purpose,
				RequestWrittenMicros: offsetPointer(m.trace.anchor),
			})
		},
		GotFirstResponseByte: func() {
			m.trace.mu.Lock()
			defer m.trace.mu.Unlock()
			for i := len(m.trace.providerAttempts) - 1; i >= 0; i-- {
				attempt := &m.trace.providerAttempts[i]
				if attempt.ModelCallID == id && attempt.HTTPAttempt == transportAttempt {
					attempt.FirstResponseByteMicros = offsetPointer(m.trace.anchor)
					break
				}
			}
		},
	})
	stream, err := m.LanguageModel.Stream(ctx, call)
	if err != nil {
		m.trace.endModelCall(id, true, ctx.Err() != nil)
		return nil, err
	}
	return func(yield func(fantasy.StreamPart) bool) {
		failed := false
		defer func() { m.trace.endModelCall(id, failed, ctx.Err() != nil) }()
		for part := range stream {
			if part.Type == fantasy.StreamPartTypeError {
				failed = true
			}
			m.trace.recordModelPart(id, part)
			started := time.Now()
			more := yield(part)
			m.trace.updateModelCall(id, func(record *notify.ModelCallTelemetry) {
				record.DeliveryMicros += time.Since(started).Microseconds()
			})
			if !more {
				return
			}
		}
	}, nil
}

func offsetPointer(anchor time.Time) *int64 {
	value := max(int64(0), time.Since(anchor).Microseconds())
	return &value
}

func (t *RunTrace) SetSession(sessionID string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.sessionID = sessionID
	t.mu.Unlock()
}

func (t *RunTrace) SetReasoning(model Model, options fantasy.ProviderOptions) {
	if t == nil {
		return
	}
	final := finalReasoningOptions(options)
	t.mu.Lock()
	t.requestedEffort = model.ModelCfg.ReasoningEffort
	t.resolvedEffort = effectiveReasoningEffort(model)
	t.reasoningEffort = final.Effort
	t.mu.Unlock()
}

func (t *RunTrace) beginModelCall(model Model, purpose string, call fantasy.Call) int {
	final := finalReasoningOptions(call.ProviderOptions)
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.modelCalls) >= maxExecutionRecords {
		t.executionRecordsDropped++
		return 0
	}
	record := notify.ModelCallTelemetry{
		ID: len(t.modelCalls) + 1, Step: t.stepCount + 1, Purpose: purpose,
		Provider: model.ModelCfg.Provider, Model: model.ModelCfg.Model,
		StartedMicros:     time.Since(t.anchor).Microseconds(),
		RequestedEffort:   model.ModelCfg.ReasoningEffort,
		RequestedThinking: model.ModelCfg.Think,
		ResolvedEffort:    effectiveReasoningEffort(model),
		FinalReasoning:    final,
	}
	if call.MaxOutputTokens != nil {
		value := *call.MaxOutputTokens
		record.MaxOutputTokens = &value
	}
	t.modelCalls = append(t.modelCalls, record)
	if purpose == "tool_loop" {
		t.reasoningEffort = final.Effort
	}
	return record.ID
}

func (t *RunTrace) updateModelCall(id int, update func(*notify.ModelCallTelemetry)) {
	if id <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	update(&t.modelCalls[id-1])
}

func (t *RunTrace) endModelCall(id int, failed, cancelled bool) {
	t.updateModelCall(id, func(record *notify.ModelCallTelemetry) {
		record.EndedMicros = offsetPointer(t.anchor)
		record.Failed = failed
		record.Cancelled = cancelled
	})
}

func (t *RunTrace) recordModelPart(id int, part fantasy.StreamPart) {
	t.updateModelCall(id, func(record *notify.ModelCallTelemetry) {
		offset := offsetPointer(t.anchor)
		if record.FirstStreamEventMicros == nil {
			record.FirstStreamEventMicros = offset
		}
		switch part.Type {
		case fantasy.StreamPartTypeReasoningStart, fantasy.StreamPartTypeReasoningDelta:
			if record.FirstReasoningMicros == nil {
				record.FirstReasoningMicros = offset
			}
		case fantasy.StreamPartTypeReasoningEnd:
			record.ReasoningEndMicros = offset
		case fantasy.StreamPartTypeToolInputStart, fantasy.StreamPartTypeToolInputDelta, fantasy.StreamPartTypeToolCall:
			if record.FirstToolMicros == nil {
				record.FirstToolMicros = offset
			}
		case fantasy.StreamPartTypeToolInputEnd:
			record.ToolInputEndMicros = offset
		case fantasy.StreamPartTypeTextDelta:
			if record.FirstTextMicros == nil {
				record.FirstTextMicros = offset
			}
		case fantasy.StreamPartTypeFinish:
			usage := part.Usage
			if !usageIsZero(usage) {
				record.InputTokens = &usage.InputTokens
				record.OutputTokens = &usage.OutputTokens
				record.ReasoningTokens = &usage.ReasoningTokens
				record.CacheReadTokens = &usage.CacheReadTokens
			}
		}
	})
}

func finalReasoningOptions(options fantasy.ProviderOptions) notify.ReasoningTelemetry {
	result := notify.ReasoningTelemetry{}
	keys := make([]string, 0, len(options))
	for key := range options {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		encoded, err := json.Marshal(options[key])
		if err != nil {
			continue
		}
		var fields map[string]any
		if json.Unmarshal(encoded, &fields) != nil {
			continue
		}
		readReasoningFields(fields, &result)
		if extra, ok := fields["extra_body"].(map[string]any); ok {
			readReasoningFields(extra, &result)
		}
	}
	return result
}

func readReasoningFields(fields map[string]any, result *notify.ReasoningTelemetry) {
	for _, key := range []string{"reasoning_effort", "effort", "thinking_level"} {
		if value, present := fields[key]; present && value == nil {
			result.Effort = ""
		}
		if value, ok := fields[key].(string); ok {
			switch value {
			case "none", "minimal", "low", "medium", "high", "xhigh", "max":
				result.Effort = value
			}
		}
	}
	if value, ok := fields["type"].(string); ok {
		switch value {
		case "enabled", "disabled", "adaptive":
			result.Thinking = value
		}
	}
	for _, key := range []string{"thinking", "enabled", "enable_thinking"} {
		if value, present := fields[key]; present && value == nil {
			result.Thinking = ""
		}
		if value, ok := fields[key].(bool); ok {
			result.Thinking = "disabled"
			if value {
				result.Thinking = "enabled"
			}
		}
	}
	for _, key := range []string{"budget_tokens", "thinking_budget"} {
		if value, present := fields[key]; present && value == nil {
			result.BudgetTokens = nil
		}
		if value, ok := fields[key].(float64); ok && value >= 0 {
			n := int64(value)
			result.BudgetTokens = &n
		}
	}
	if value, present := fields["clear_thinking"]; present && value == nil {
		result.ClearThinking = nil
	}
	if value, ok := fields["clear_thinking"].(bool); ok {
		result.ClearThinking = &value
	}
	if value, present := fields["tool_stream"]; present && value == nil {
		result.ToolStream = nil
	}
	if value, ok := fields["tool_stream"].(bool); ok {
		result.ToolStream = &value
	}
	for _, key := range []string{"reasoning", "thinking", "thinking_config", "chat_template_args"} {
		if value, present := fields[key]; present && value == nil {
			switch key {
			case "reasoning":
				result.Effort = ""
			case "thinking", "thinking_config":
				result.Thinking = ""
				result.BudgetTokens = nil
			}
		}
		if child, ok := fields[key].(map[string]any); ok {
			readReasoningFields(child, result)
		}
	}
}

type tracedTool struct {
	fantasy.AgentTool
	trace     *RunTrace
	hooks     *hooks.Runner
	sessionID string
}

func (tool tracedTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	t := tool.trace
	t.mu.Lock()
	record := notify.ToolCallTelemetry{ID: call.ID, Name: call.Name, Step: t.stepCount + 1, StartedMicros: time.Since(t.anchor).Microseconds()}
	t.mu.Unlock()
	ctx = runobserve.WithPhases(ctx, func(phase string, duration time.Duration) {
		t.mu.Lock()
		defer t.mu.Unlock()
		switch phase {
		case "hook":
			record.HookMicros += duration.Microseconds()
		case "permission":
			record.PermissionMicros += duration.Microseconds()
		}
	})

	agg, denied := tool.runPreToolUseHooks(ctx, &call)
	if denied {
		response := mergeHookMetadata(deniedHookResponse(agg), agg)
		t.recordToolCall(&record, response, nil)
		return response, nil
	}

	response, err := tool.AgentTool.Run(ctx, call)
	if agg.HookCount > 0 && agg.Context != "" {
		response.Content = joinHookContext(response.Content, agg.Context)
	}
	response = mergeHookMetadata(response, agg)
	t.recordToolCall(&record, response, err)
	return response, err
}

// runPreToolUseHooks runs this workspace's PreToolUse hooks for the call and
// reports whether the call must be denied. It also applies an updated_input
// patch to the call in place, so a hook can rewrite a tool's arguments before
// it runs. The hook wall time is recorded as the "hook" phase so it shows up
// in per-tool telemetry. Hooks never fail the call: a runner error is logged
// and treated as "no opinion", matching the exit-code semantics in the hooks
// package where only exit codes 2 and 49 block.
func (tool tracedTool) runPreToolUseHooks(ctx context.Context, call *fantasy.ToolCall) (hooks.AggregateResult, bool) {
	if tool.hooks == nil {
		return hooks.AggregateResult{}, false
	}

	stopHook := runobserve.Start(ctx, "hook")
	agg, err := tool.hooks.Run(ctx, hooks.EventPreToolUse, tool.sessionID, call.Name, call.Input)
	stopHook()
	if err != nil {
		slog.Warn("PreToolUse hooks failed; continuing without a hook decision", "tool", call.Name, "error", err)
		return hooks.AggregateResult{}, false
	}
	if agg.Decision == hooks.DecisionDeny {
		return agg, true
	}
	if agg.UpdatedInput != "" {
		call.Input = agg.UpdatedInput
	}
	return agg, false
}

// deniedHookResponse builds the tool response for a denied call. The hook's
// reason is the tool error text; a halting hook (exit code 49) additionally
// stops the turn so the model is not called again.
func deniedHookResponse(agg hooks.AggregateResult) fantasy.ToolResponse {
	reason := strings.TrimSpace(agg.Reason)
	if reason == "" {
		reason = "blocked by PreToolUse hook"
	}
	return fantasy.ToolResponse{
		Type:     "text",
		Content:  reason,
		IsError:  true,
		StopTurn: agg.Halt,
	}
}

// joinHookContext appends hook-provided context to the tool's own output so
// the model sees it on the next step.
func joinHookContext(content, hookContext string) string {
	if content == "" {
		return hookContext
	}
	return content + "\n\n" + hookContext
}

// mergeHookMetadata records the hook outcome in the tool response metadata so
// clients can render a hook indicator. Existing metadata keys (status,
// exit_code) are preserved.
func mergeHookMetadata(response fantasy.ToolResponse, agg hooks.AggregateResult) fantasy.ToolResponse {
	if agg.HookCount == 0 {
		return response
	}

	merged := map[string]any{}
	if response.Metadata != "" {
		if err := json.Unmarshal([]byte(response.Metadata), &merged); err != nil {
			slog.Warn("Tool response metadata is not a JSON object; dropping it for hook metadata", "error", err)
			merged = map[string]any{}
		}
	}

	hookMeta := hooks.HookMetadata{
		HookCount:    agg.HookCount,
		Decision:     agg.Decision.String(),
		Halt:         agg.Halt,
		Reason:       agg.Reason,
		InputRewrite: agg.UpdatedInput != "",
		Hooks:        agg.Hooks,
	}
	encoded, err := json.Marshal(hookMeta)
	if err != nil {
		slog.Warn("Failed to encode hook metadata", "error", err)
		return response
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		slog.Warn("Failed to decode hook metadata", "error", err)
		return response
	}
	for key, value := range fields {
		merged[key] = value
	}

	out, err := json.Marshal(merged)
	if err != nil {
		slog.Warn("Failed to merge hook metadata", "error", err)
		return response
	}
	response.Metadata = string(out)
	return response
}

// recordToolCall finishes the telemetry record and appends it to the trace.
func (t *RunTrace) recordToolCall(record *notify.ToolCallTelemetry, response fantasy.ToolResponse, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	record.EndedMicros = time.Since(t.anchor).Microseconds()
	record.IsError = err != nil || response.IsError
	var metadata struct {
		Status   string `json:"status"`
		ExitCode *int   `json:"exit_code"`
	}
	if json.Unmarshal([]byte(response.Metadata), &metadata) == nil {
		switch metadata.Status {
		case "running", "completed", "failed", "cancelled":
			record.Status = metadata.Status
		}
		record.ExitCode = metadata.ExitCode
	}
	if len(t.toolCalls) < maxExecutionRecords {
		t.toolCalls = append(t.toolCalls, *record)
	} else {
		t.executionRecordsDropped++
	}
}

// traceTools wraps every tool the agent can call with telemetry and the
// workspace's PreToolUse hook runner.
func traceTools(tools []fantasy.AgentTool, trace *RunTrace, hookRunner *hooks.Runner, sessionID string) []fantasy.AgentTool {
	wrapped := make([]fantasy.AgentTool, len(tools))
	for i, tool := range tools {
		wrapped[i] = tracedTool{AgentTool: tool, trace: trace, hooks: hookRunner, sessionID: sessionID}
	}
	return wrapped
}

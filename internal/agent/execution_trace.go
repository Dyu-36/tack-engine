package agent

import (
	"context"
	"encoding/json"
	"net/http/httptrace"
	"sort"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/notify"
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
	trace *RunTrace
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
	response, err := tool.AgentTool.Run(ctx, call)
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
		t.toolCalls = append(t.toolCalls, record)
	} else {
		t.executionRecordsDropped++
	}
	return response, err
}

func traceTools(tools []fantasy.AgentTool, trace *RunTrace) []fantasy.AgentTool {
	wrapped := make([]fantasy.AgentTool, len(tools))
	for i, tool := range tools {
		wrapped[i] = tracedTool{AgentTool: tool, trace: trace}
	}
	return wrapped
}

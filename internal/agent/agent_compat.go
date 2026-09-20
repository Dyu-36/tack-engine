package agent

// AgentToolName is retained for UI compatibility; the sub-agent tool is no
// longer built by the default registry.
const AgentToolName = "agent"

// AgentParams is the wire schema retained so the TUI can still render
// historical sub-agent tool calls from old sessions.
type AgentParams struct {
	Prompt string `json:"prompt"`
}

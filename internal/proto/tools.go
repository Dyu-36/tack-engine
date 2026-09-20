package proto

// The wire schema for per-tool parameters is owned by the tool itself, not
// duplicated here. We alias the canonical names so there is exactly one
// source of truth.
import "github.com/charmbracelet/crush/internal/agent/tools"

// ToolResponseType represents the type of tool response.
type ToolResponseType string

const (
	ToolResponseTypeText  ToolResponseType = "text"
	ToolResponseTypeImage ToolResponseType = "image"
)

// ToolResponse represents a response from a tool.
type ToolResponse struct {
	Type     ToolResponseType `json:"type"`
	Content  string           `json:"content"`
	Metadata string           `json:"metadata,omitempty"`
	IsError  bool             `json:"is_error"`
}

// BashToolName mirrors the shell tool's registry name (powershell on Windows).
const BashToolName = tools.BashToolName

// BashParams represents the parameters for the bash tool.
type BashParams struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// BashResponseMetadata represents the metadata for a bash tool response.
type BashResponseMetadata struct {
	StartTime        int64  `json:"start_time"`
	EndTime          int64  `json:"end_time"`
	Output           string `json:"output"`
	WorkingDirectory string `json:"working_directory"`
}

const EditToolName = "edit"

// EditParams represents the parameters for the edit tool.
type EditParams struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// EditResponseMetadata represents the metadata for an edit tool response.
type EditResponseMetadata struct {
	Additions  int    `json:"additions"`
	Removals   int    `json:"removals"`
	OldContent string `json:"old_content,omitempty"`
	NewContent string `json:"new_content,omitempty"`
}

// ViewToolName mirrors the read tool's registry name.
const ViewToolName = tools.ViewToolName

// ViewParams represents the parameters for the view tool.
type ViewParams struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
}

// ViewResponseMetadata represents the metadata for a view tool response.
type ViewResponseMetadata struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

const WriteToolName = "write"

// WriteParams represents the parameters for the write tool.
type WriteParams struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// WriteResponseMetadata represents the metadata for a write tool response.
type WriteResponseMetadata struct {
	Diff      string `json:"diff"`
	Additions int    `json:"additions"`
	Removals  int    `json:"removals"`
}

package tools

import (
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/shell"
)

type BashParams struct {
	Command    string `json:"command" description:"The command to execute"`
	WorkingDir string `json:"working_dir,omitempty" description:"The working directory to execute the command in (defaults to the session working directory)"`
	Timeout    int    `json:"timeout,omitempty" description:"Seconds before the command is killed (default 120)"`
	// RunInBackground is a retained no-op for UI compatibility; commands always run in the foreground.
	RunInBackground bool `json:"run_in_background,omitempty"`
}

type BashResponseMetadata struct {
	Status           string `json:"status"`
	ExitCode         *int   `json:"exit_code,omitempty"`
	StartTime        int64  `json:"start_time"`
	EndTime          int64  `json:"end_time"`
	Output           string `json:"output"`
	WorkingDirectory string `json:"working_directory"`
	// Retained for UI compatibility; always zero-value for the foreground shell.
	Description string `json:"description,omitempty"`
	Background  bool   `json:"background,omitempty"`
	ShellID     string `json:"shell_id,omitempty"`
}

const (
	BashToolName = "powershell"

	DefaultShellTimeout = 120
	MaxOutputLength     = 30000
	ShellNoOutput       = "no output"
)

//go:embed bash.md.tpl
var bashDescriptionTmpl []byte

var bashDescriptionTpl = template.Must(
	template.New("bashDescription").
		Parse(string(bashDescriptionTmpl)),
)

type bashDescriptionData struct {
	MaxOutputLength int
}

func bashDescription() string {
	var out bytes.Buffer
	if err := bashDescriptionTpl.Execute(&out, bashDescriptionData{
		MaxOutputLength: MaxOutputLength,
	}); err != nil {
		panic("failed to execute bash description template: " + err.Error())
	}
	return out.String()
}

func NewBashTool(workingDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		BashToolName,
		bashDescription(),
		func(ctx context.Context, params BashParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if strings.TrimSpace(params.Command) == "" {
				return fantasy.NewTextErrorResponse("missing command"), nil
			}

			execWorkingDir := cmp.Or(params.WorkingDir, workingDir)
			startTime := time.Now()

			timeout := time.Duration(cmp.Or(params.Timeout, DefaultShellTimeout)) * time.Second
			if timeout <= 0 {
				timeout = DefaultShellTimeout * time.Second
			}
			runCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			stdout, stderr, execErr := execShell(runCtx, execWorkingDir, params.Command)

			output := formatOutput(stdout, stderr, execErr)
			metadata := BashResponseMetadata{
				StartTime:        startTime.UnixMilli(),
				EndTime:          time.Now().UnixMilli(),
				Output:           output,
				WorkingDirectory: execWorkingDir,
			}
			metadata.Status, metadata.ExitCode = shellCompletionStatus(execErr)
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				metadata.Status, metadata.ExitCode = "cancelled", nil
				if !strings.Contains(output, "aborted") {
					output = strings.TrimRight(output, "\n") + "\nCommand timed out and was aborted"
				}
			}
			if output == "" {
				output = ShellNoOutput
			}
			output += fmt.Sprintf("\n\n<cwd>%s</cwd>", normalizeWorkingDir(execWorkingDir))

			response := fantasy.NewTextResponse(output)
			if metadata.Status != "completed" {
				response = fantasy.NewTextErrorResponse(output)
			}
			return fantasy.WithResponseMetadata(response, metadata), nil
		},
	)
}

// execShell runs the command in the foreground and returns stdout, stderr and
// the execution error. On Windows it invokes powershell.exe directly with
// UTF-8 output; other platforms use the mvdan/sh interpreter.
func execShell(ctx context.Context, workingDir, command string) (string, string, error) {
	if runtime.GOOS == "windows" {
		return execPowerShell(ctx, workingDir, command)
	}
	sh := shell.NewShell(&shell.Options{WorkingDir: workingDir})
	return sh.Exec(ctx, command)
}

// execPowerShell runs a command via Windows PowerShell with UTF-8 console
// output. The context controls cancellation: when the caller stops or the
// timeout fires, the process tree is killed.
func execPowerShell(ctx context.Context, workingDir, command string) (string, string, error) {
	wrapped := "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; " +
		"[Console]::InputEncoding = [System.Text.Encoding]::UTF8; " +
		command +
		"; if ($null -ne $LASTEXITCODE) { exit $LASTEXITCODE }"

	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-Command", wrapped)
	cmd.Dir = workingDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func shellCompletionStatus(execErr error) (string, *int) {
	if shell.IsInterrupt(execErr) {
		return "cancelled", nil
	}
	exitCode := exitCodeOf(execErr)
	if execErr != nil || exitCode != 0 {
		if exitCode == 0 {
			return "failed", nil
		}
		return "failed", &exitCode
	}
	return "completed", &exitCode
}

// exitCodeOf extracts a process exit code, understanding both the mvdan/sh
// interpreter status (non-Windows path) and os/exec.ExitError (PowerShell).
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return shell.ExitCode(err)
}

// formatOutput formats the output of a completed command with error handling.
func formatOutput(stdout, stderr string, execErr error) string {
	interrupted := shell.IsInterrupt(execErr)
	exitCode := exitCodeOf(execErr)

	stdout = truncateOutput(stdout)
	stderr = truncateOutput(stderr)

	errorMessage := stderr
	if errorMessage == "" && execErr != nil {
		errorMessage = execErr.Error()
	}

	if interrupted {
		if errorMessage != "" {
			errorMessage += "\n"
		}
		errorMessage += "Command was aborted before completion"
	} else if exitCode != 0 {
		if errorMessage != "" {
			errorMessage += "\n"
		}
		errorMessage += fmt.Sprintf("Exit code %d", exitCode)
	}

	if stdout != "" && stderr != "" {
		stdout += "\n"
	}
	if errorMessage != "" {
		stdout += "\n" + errorMessage
	}
	return stdout
}

func TruncateOutput(content string) string {
	if ansi.StringWidth(content) <= MaxOutputLength {
		return content
	}

	halfLength := MaxOutputLength / 2
	start := ansi.Truncate(content, halfLength, "")
	end := ansi.TruncateLeft(content, ansi.StringWidth(content)-halfLength, "")

	truncatedLinesCount := max(strings.Count(content, "\n")-strings.Count(start, "\n")-strings.Count(end, "\n"), 0)
	return fmt.Sprintf("%s\n\n... [%d lines truncated] ...\n\n%s", start, truncatedLinesCount, end)
}

func truncateOutput(content string) string {
	return TruncateOutput(content)
}

func normalizeWorkingDir(path string) string {
	if runtime.GOOS == "windows" {
		path = strings.ReplaceAll(path, fsext.WindowsWorkingDirDrive(), "")
	}
	return filepath.ToSlash(path)
}

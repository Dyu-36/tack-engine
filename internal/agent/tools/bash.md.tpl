Execute a shell command in the foreground and return its output.

<behavior>
- Runs via `powershell.exe` on Windows (UTF-8 console), via a POSIX-compatible shell elsewhere.
- Executes in the session working directory; `working_dir` overrides it for one call.
- Output is truncated to {{ .MaxOutputLength }} characters; the exit code is always reported.
- Set `timeout` (seconds) for long commands; when the user stops the run the process tree is killed. Commands never move to a background job.
</behavior>

<usage_notes>
- Prefer the dedicated tools (view, grep, glob, ls) over cat/find/grep/ls shell equivalents when reading or searching files.
- Use absolute paths; each command runs in its own process with no state carried between calls.
</usage_notes>

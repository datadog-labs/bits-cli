package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	exectool "github.com/DataDog/bits-cli/internal/tools/exec"
)

const execCommandDescription = "Run one shell command and return its final stdout, stderr, and exit status. This V1 is unsandboxed: it inherits Bits' normal environment and can access your filesystem, network, credentials, and agent sockets. Commands receive no stdin, time out after 10 seconds, and run concurrently (up to four), so workspace effects may race. Descendant cleanup is best effort."

type execRunner interface {
	Run(context.Context, exectool.ExecRequest) exectool.ExecOutcome
}

type execCommandArgs struct {
	Command string `json:"cmd"`
	Workdir string `json:"workdir,omitempty"`
}

type execCommandResult struct {
	Status             exectool.ExecTerminalReason `json:"status"`
	ExitCode           *int                        `json:"exit_code,omitempty"`
	Signal             string                      `json:"signal,omitempty"`
	DurationMS         int64                       `json:"duration_ms"`
	Truncated          bool                        `json:"truncated"`
	OutputIncomplete   bool                        `json:"output_incomplete"`
	StdoutOmittedBytes int64                       `json:"stdout_omitted_bytes"`
	StderrOmittedBytes int64                       `json:"stderr_omitted_bytes"`
	Error              string                      `json:"error,omitempty"`
	Stdout             string                      `json:"stdout"`
	Stderr             string                      `json:"stderr"`
}

func newExecCommandTool(turnCWD string, runner execRunner) agent.Tool {
	return agent.Tool{
		Definition: assistant.ClientTool{
			Name:        toolExec,
			Description: execCommandDescription,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"cmd": map[string]any{
						"type":        "string",
						"minLength":   1,
						"description": "Shell command to execute.",
					},
					"workdir": map[string]any{
						"type":        "string",
						"description": "Working directory. Defaults to the turn working directory; relative paths resolve from it.",
					},
				},
				"required":             []string{"cmd"},
				"additionalProperties": false,
			},
		},
		Approval: execCommandApproval(turnCWD),
		Handler:  execCommandHandler(turnCWD, runner),
	}
}

func execCommandApproval(turnCWD string) agent.ApprovalPolicy {
	return func(call agent.ToolCall) (agent.ApprovalRequirement, bool) {
		args, cwd, err := parseExecCommandArgs(call.Input, turnCWD)
		if err != nil {
			// Invalid input cannot launch and therefore needs no permission prompt;
			// the handler returns the validation error to the model.
			return agent.ApprovalRequirement{}, false
		}
		return agent.ApprovalRequirement{
			Key: execCommandApprovalKey(args, cwd),
			Prompt: agent.ApprovalPrompt{
				Title: "Run an unsandboxed command?",
				Detail: fmt.Sprintf(
					"cmd: %s · cwd: %s · unsandboxed",
					args.Command, cwd,
				),
			},
		}, true
	}
}

func execCommandHandler(turnCWD string, runner execRunner) agent.ToolHandler {
	return func(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return agent.ToolResult{}, err
		}
		args, cwd, err := parseExecCommandArgs(call.Input, turnCWD)
		if err != nil {
			outcome := exectool.ExecOutcome{
				Reason: exectool.ExecLaunchFailed,
				Err:    fmt.Errorf("invalid input: %w", err),
			}
			return agent.ToolResult{
				Title:   "Invalid command",
				Output:  marshalExecCommandResult(outcome),
				IsError: true,
			}, nil
		}
		outcome := runner.Run(ctx, exectool.ExecRequest{Command: args.Command, CWD: cwd})
		output := marshalExecCommandResult(outcome)
		return agent.ToolResult{
			Title:     execResultTitle(outcome),
			Output:    output,
			IsError:   outcome.Reason != exectool.ExecSucceeded,
			Cancelled: outcome.Reason == exectool.ExecCancelled,
		}, nil
	}
}

func parseExecCommandArgs(input, turnCWD string) (execCommandArgs, string, error) {
	var args execCommandArgs
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return execCommandArgs{}, "", err
	}
	if args.Command == "" {
		return execCommandArgs{}, "", fmt.Errorf("cmd must not be empty")
	}
	workdir := args.Workdir
	if workdir == "" {
		workdir = turnCWD
	} else if !filepath.IsAbs(workdir) {
		workdir = filepath.Join(turnCWD, workdir)
	}
	return args, filepath.Clean(workdir), nil
}

func execCommandApprovalKey(args execCommandArgs, cwd string) agent.ApprovalKey {
	// JSON encoding makes the tuple framing unambiguous even when either value
	// contains control characters. The digest keeps arbitrary commands out of
	// the authority-map key while making a session grant exact to this launch.
	encoded, err := json.Marshal([2]string{args.Command, cwd})
	if err != nil {
		panic("marshal exec approval authority: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return agent.ApprovalKey{Tool: toolExec, Resource: fmt.Sprintf("%x", digest)}
}

func execResultTitle(outcome exectool.ExecOutcome) string {
	switch outcome.Reason {
	case exectool.ExecSucceeded:
		return "Command completed"
	case exectool.ExecNonZeroExit:
		return "Command exited unsuccessfully"
	case exectool.ExecLaunchFailed:
		return "Command could not start"
	case exectool.ExecTimedOut:
		return "Command timed out"
	case exectool.ExecCancelled:
		return "Command cancelled"
	default:
		return "Command finished"
	}
}

func marshalExecCommandResult(outcome exectool.ExecOutcome) string {
	result := execCommandResult{
		Status:             outcome.Reason,
		ExitCode:           outcome.ExitCode,
		Signal:             outcome.Signal,
		DurationMS:         outcome.Elapsed.Milliseconds(),
		Stdout:             outcome.Output.Stdout,
		Stderr:             outcome.Output.Stderr,
		Truncated:          outcome.Output.Truncated,
		OutputIncomplete:   outcome.Output.Incomplete,
		StdoutOmittedBytes: outcome.Output.StdoutOmittedBytes,
		StderrOmittedBytes: outcome.Output.StderrOmittedBytes,
	}
	if outcome.Err != nil {
		result.Error = outcome.Err.Error()
	}

	return string(mustMarshalExecResult(result))
}

func mustMarshalExecResult(result execCommandResult) []byte {
	encoded, err := json.Marshal(result)
	if err != nil {
		panic("marshal exec command result: " + err.Error())
	}
	return encoded
}

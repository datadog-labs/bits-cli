package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	exectool "github.com/DataDog/bits-cli/internal/tools/exec"
	"github.com/DataDog/bits-cli/internal/tools/spec"
)

const execCommandDescription = "Runs a non-interactive shell command to completion and returns its stdout, stderr, and terminal status. The process is terminated on timeout or cancellation and cannot be resumed."

type execRunner interface {
	Run(context.Context, exectool.ExecRequest) exectool.ExecOutcome
}

func newExecCommandTool(turnCWD string, runner execRunner) agent.Tool {
	return agent.Tool{
		Definition: assistant.ClientTool{
			Name:        spec.ExecCommand,
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
					"timeout_ms": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"maximum":     spec.ExecMaxTimeoutMS,
						"description": "Maximum command runtime. Defaults to 10000 ms; maximum 600000 ms.",
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
					"cmd: %s · cwd: %s%s · unsandboxed",
					args.Cmd, cwd, execTimeoutApprovalDetail(args),
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
		outcome := runner.Run(ctx, resolvedExecCommandRequest(args, cwd))
		output := marshalExecCommandResult(outcome)
		return agent.ToolResult{
			Title:     execResultTitle(outcome),
			Output:    output,
			IsError:   outcome.Reason != exectool.ExecSucceeded,
			Cancelled: outcome.Reason == exectool.ExecCancelled,
		}, nil
	}
}

func parseExecCommandArgs(input, turnCWD string) (spec.ExecCommandInput, string, error) {
	var args spec.ExecCommandInput
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return spec.ExecCommandInput{}, "", err
	}
	if args.Cmd == "" {
		return spec.ExecCommandInput{}, "", fmt.Errorf("cmd must not be empty")
	}
	if args.TimeoutMS != nil && (*args.TimeoutMS < 1 || *args.TimeoutMS > spec.ExecMaxTimeoutMS) {
		return spec.ExecCommandInput{}, "", fmt.Errorf("timeout_ms must be between 1 and %d", spec.ExecMaxTimeoutMS)
	}
	workdir := args.Workdir
	if workdir == "" {
		workdir = turnCWD
	} else if !filepath.IsAbs(workdir) {
		workdir = filepath.Join(turnCWD, workdir)
	}
	return args, filepath.Clean(workdir), nil
}

func execCommandApprovalKey(args spec.ExecCommandInput, cwd string) agent.ApprovalKey {
	// JSON encoding makes the tuple framing unambiguous even when either value
	// contains control characters. The digest keeps arbitrary commands out of
	// the authority-map key while making a session grant exact to this launch.
	encoded, err := json.Marshal(resolvedExecCommandRequest(args, cwd))
	if err != nil {
		panic("marshal exec approval authority: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return agent.ApprovalKey{Tool: spec.ExecCommand, Resource: fmt.Sprintf("%x", digest)}
}

func resolvedExecCommandRequest(args spec.ExecCommandInput, cwd string) exectool.ExecRequest {
	request := exectool.ExecRequest{Command: args.Cmd, CWD: cwd}
	if args.TimeoutMS != nil {
		request.Timeout = time.Duration(*args.TimeoutMS) * time.Millisecond
	}
	return request
}

func execTimeoutApprovalDetail(args spec.ExecCommandInput) string {
	if args.TimeoutMS == nil {
		return ""
	}
	return " · timeout: " + (time.Duration(*args.TimeoutMS) * time.Millisecond).String()
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
	result := spec.ExecCommandOutput{
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

func mustMarshalExecResult(result spec.ExecCommandOutput) []byte {
	encoded, err := json.Marshal(result)
	if err != nil {
		panic("marshal exec command result: " + err.Error())
	}
	return encoded
}

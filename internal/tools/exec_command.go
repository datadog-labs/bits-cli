package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	exectool "github.com/datadog-labs/bits-cli/internal/tools/exec"
	"github.com/datadog-labs/bits-cli/internal/tools/spec"
)

const (
	execCommandDescription = "Runs a command in a non-interactive shell and returns its stdout, stderr, and exit status. " +
		"A command still running after yield_ms keeps running in the background: the result then has its output so far and a command_id " +
		"for wait_command and stop_command. It runs until it exits, timeout_ms elapses, or stop_command ends it; " +
		"Bits also stops it when Bits exits or the conversation changes. " +
		"Run servers and other long-lived processes directly, never with & or nohup: Bits supervises the command it starts, not processes it leaves behind."
	waitCommandDescription = "Waits up to wait_ms for a command exec_command left running to exit. " +
		"Returns its output since the previous result and, once it exited, its exit status. Prefer long waits to frequent polling."
	stopCommandDescription = "Stops a command exec_command left running and returns its remaining output and final status."
)

type execRunner interface {
	Start(context.Context, exectool.ExecRequest, time.Duration) exectool.ExecOutcome
	Wait(context.Context, string, time.Duration) (exectool.ExecOutcome, error)
	Stop(context.Context, string) (exectool.ExecOutcome, error)
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
						"description": "Command in the selected shell's syntax. Windows defaults to PowerShell; request shell bash for Bash syntax.",
					},
					"shell": map[string]any{
						"type":        "string",
						"description": "Shell executable name on PATH or explicit path. Defaults to <shell>. Windows supports pwsh, powershell, and bash (when installed); Unix supports bash, zsh, and sh.",
					},
					"workdir": map[string]any{
						"type":        "string",
						"description": "Working directory. Defaults to <cwd>; relative paths resolve from it.",
					},
					"timeout_ms": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"description": "Maximum command runtime, after which it is stopped. Omit for no limit.",
					},
					"yield_ms": map[string]any{
						"type":        "integer",
						"minimum":     0,
						"maximum":     spec.ExecMaxWaitMS,
						"description": fmt.Sprintf("How long to wait before returning while the command keeps running; not a timeout. Defaults to %d; 0 returns once it starts.", spec.ExecDefaultYieldMS),
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

// Waiting on or stopping a command needs no approval: launching it already
// authorized sharing its output, and stopping only takes authority away.
func newWaitCommandTool(runner execRunner) agent.Tool {
	return agent.Tool{
		Definition: assistant.ClientTool{
			Name:        spec.WaitCommand,
			Description: waitCommandDescription,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command_id": commandIDSchema,
					"wait_ms": map[string]any{
						"type":        "integer",
						"minimum":     0,
						"maximum":     spec.ExecMaxWaitMS,
						"description": fmt.Sprintf("Longest time to wait for the exit. Defaults to %d.", spec.ExecDefaultWaitMS),
					},
				},
				"required":             []string{"command_id"},
				"additionalProperties": false,
			},
		},
		Handler: func(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
			var args spec.WaitCommandInput
			if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
				return invalidExecControlResult(err), nil
			}
			wait := spec.ExecDefaultWaitMS
			if args.WaitMS != nil {
				wait = *args.WaitMS
			}
			if wait < 0 || wait > spec.ExecMaxWaitMS {
				return invalidExecControlResult(fmt.Errorf("wait_ms must be between 0 and %d", spec.ExecMaxWaitMS)), nil
			}
			outcome, err := runner.Wait(ctx, args.CommandID, time.Duration(wait)*time.Millisecond)
			if err != nil {
				return execControlFailure(err)
			}
			return execObservedResult(outcome), nil
		},
	}
}

func newStopCommandTool(runner execRunner) agent.Tool {
	return agent.Tool{
		Definition: assistant.ClientTool{
			Name:        spec.StopCommand,
			Description: stopCommandDescription,
			InputSchema: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"command_id": commandIDSchema},
				"required":             []string{"command_id"},
				"additionalProperties": false,
			},
		},
		Handler: func(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
			var args spec.StopCommandInput
			if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
				return invalidExecControlResult(err), nil
			}
			outcome, err := runner.Stop(ctx, args.CommandID)
			if err != nil {
				return execControlFailure(err)
			}
			result := execObservedResult(outcome)
			if outcome.Status == exectool.ExecCancelled {
				result.Title = "Command stopped"
			}
			return result, nil
		},
	}
}

var commandIDSchema = map[string]any{
	"type":        "string",
	"minLength":   1,
	"description": "The command_id exec_command returned.",
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
					"cwd: %s%s · unsandboxed",
					cwd, execTimeoutApprovalDetail(args),
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
				Status: exectool.ExecLaunchFailed,
				Err:    fmt.Errorf("invalid input: %w", err),
			}
			return agent.ToolResult{
				Title:   "Invalid command",
				Output:  marshalExecCommandResult(outcome),
				IsError: true,
			}, nil
		}
		yield := spec.ExecDefaultYieldMS
		if args.YieldMS != nil {
			yield = *args.YieldMS
		}
		outcome := runner.Start(ctx, resolvedExecCommandRequest(args, cwd), time.Duration(yield)*time.Millisecond)
		return execCommandResult(outcome), nil
	}
}

// execCommandResult reports outcome as the tool result. A running command is
// not an error: it is answering early by request.
func execCommandResult(outcome exectool.ExecOutcome) agent.ToolResult {
	return agent.ToolResult{
		Title:     execResultTitle(outcome),
		Output:    marshalExecCommandResult(outcome),
		IsError:   outcome.Status != exectool.ExecSucceeded && outcome.Status != exectool.ExecRunning,
		Cancelled: outcome.Status == exectool.ExecCancelled,
	}
}

// execObservedResult reports what a wait or stop observed. A stopped command
// is not the call's failure: someone meant to stop it, and the output says
// who.
func execObservedResult(outcome exectool.ExecOutcome) agent.ToolResult {
	result := execCommandResult(outcome)
	if outcome.Status == exectool.ExecCancelled {
		result.IsError, result.Cancelled = false, false
	}
	return result
}

// execControlFailure reports a wait or stop that could not observe its
// command. The call's own cancellation propagates as for any handler.
func execControlFailure(err error) (agent.ToolResult, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return agent.ToolResult{}, err
	}
	return agent.ToolResult{
		Title:   "Unknown command",
		Output:  err.Error() + ": no such command is running. Bits stops its commands when it exits or the conversation changes, and returns a command's final result once.",
		IsError: true,
	}, nil
}

func invalidExecControlResult(err error) agent.ToolResult {
	return agent.ToolResult{Title: "Invalid command id", Output: "invalid input: " + err.Error(), IsError: true}
}

func parseExecCommandArgs(input, turnCWD string) (spec.ExecCommandInput, string, error) {
	var args spec.ExecCommandInput
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return spec.ExecCommandInput{}, "", err
	}
	if args.Cmd == "" {
		return spec.ExecCommandInput{}, "", fmt.Errorf("cmd must not be empty")
	}
	if args.TimeoutMS != nil && *args.TimeoutMS < 1 {
		return spec.ExecCommandInput{}, "", fmt.Errorf("timeout_ms must be at least 1")
	}
	if args.YieldMS != nil && (*args.YieldMS < 0 || *args.YieldMS > spec.ExecMaxWaitMS) {
		return spec.ExecCommandInput{}, "", fmt.Errorf("yield_ms must be between 0 and %d", spec.ExecMaxWaitMS)
	}
	workdir := args.Workdir
	if workdir == "" {
		workdir = turnCWD
	} else if !filepath.IsAbs(workdir) {
		workdir = filepath.Join(turnCWD, workdir)
	}
	return args, filepath.Clean(workdir), nil
}

// execCommandApprovalKey covers what runs, not when the call answers: the
// yield is deliberately absent from the request it digests.
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
	request := exectool.ExecRequest{Command: args.Cmd, CWD: cwd, Shell: args.Shell}
	if args.TimeoutMS != nil {
		request.Timeout = time.Duration(*args.TimeoutMS) * time.Millisecond
	}
	return request
}

// execTimeoutApprovalDetail states the runtime being approved: without a
// timeout, a command may keep running after its call answers.
func execTimeoutApprovalDetail(args spec.ExecCommandInput) string {
	if args.TimeoutMS == nil {
		return " · no time limit"
	}
	return " · timeout: " + (time.Duration(*args.TimeoutMS) * time.Millisecond).String()
}

func execResultTitle(outcome exectool.ExecOutcome) string {
	switch outcome.Status {
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
	case exectool.ExecRunning:
		return "Command running"
	}
	return "Command finished"
}

func marshalExecCommandResult(outcome exectool.ExecOutcome) string {
	result := spec.ExecCommandOutput{
		Status:             outcome.Status,
		CommandID:          outcome.ID,
		Command:            outcome.Command,
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

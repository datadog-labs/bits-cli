package viz

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPClient supplies authenticated MCP tool results.
// It must support concurrent calls and honor cancellation. Ownership stays with
// the caller; credentials must never be passed through the viewer environment.
type MCPClient interface {
	CallTool(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error)
}

type viewer interface {
	present(*mcp.CallToolResult) error
	snapshot(context.Context) ([]byte, error)
	show() error
	close()
	exited() bool
}

type launchFunc func(context.Context, context.Context, bool) (viewer, error)

type wireMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *wireError      `json:"error,omitempty"`
}

type wireError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type process struct {
	input       io.WriteCloser
	writeMu     sync.Mutex
	cancel      context.CancelFunc
	kill        func() error
	done, ready chan struct{}
	replies     chan wireMessage
	busy        atomic.Int64
	activity    atomic.Int64
	failed      atomic.Bool
	once        sync.Once
}

// startError preserves exec's failure for operation-level classification.
type startError struct{ err error }

func (e *startError) Error() string { return e.err.Error() }

func (e *startError) Unwrap() error { return e.err }

func launch(lifetime, startup context.Context, binary string, config Config, headless bool) (viewer, error) {
	return launchArgs(lifetime, startup, binary, nil, config, headless)
}

func launchArgs(lifetime, startup context.Context, binary string, args []string, config Config, headless bool) (viewer, error) {
	ctx, cancel := context.WithCancel(lifetime)
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = viewerEnv(config, headless)
	cmd.Stderr = io.Discard
	if config.Debug {
		cmd.Stderr = os.Stderr
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		cancel()
		return nil, err
	}
	cmd.Cancel = input.Close
	cmd.WaitDelay = 2 * time.Second
	if err = cmd.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		cancel()
		return nil, &startError{err: err}
	}
	p := &process{
		input:   input,
		cancel:  cancel,
		kill:    cmd.Process.Kill,
		done:    make(chan struct{}),
		ready:   make(chan struct{}),
		replies: make(chan wireMessage, 1),
	}
	p.activity.Store(time.Now().UnixNano())
	go p.readMessages(ctx, output, cmd, config.MCP)
	if err := p.waitReady(startup); err != nil {
		p.close()
		return nil, err
	}
	return p, nil
}

func viewerEnv(config Config, headless bool) []string {
	var env []string
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "USER", "LOGNAME", "__CFBundleIdentifier", "DO_NOT_TRACK", "DISABLE_TELEMETRY"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	env = append(env,
		"DDVIZ_IPC=stdio",
		"DDVIZ_OPEN=on-demand",
		"DDVIZ_PARENT_PID="+strconv.Itoa(os.Getpid()),
		"DDVIZ_FRAME_SRC="+config.FrameURL,
		"DDVIZ_VERSION="+config.Version,
	)
	if headless {
		env = append(env, "DDVIZ_HEADLESS=1")
	}
	if config.Debug {
		env = append(env, "DDVIZ_DEBUG=1", "DDVIZ_DATA_DIR="+config.DataDir)
	}
	return env
}

func (p *process) readMessages(ctx context.Context, output io.Reader, cmd *exec.Cmd, client MCPClient) {
	defer close(p.done)
	defer p.cancel()
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 65536), 32<<20)
	var callbacks sync.WaitGroup
	for scanner.Scan() {
		var msg wireMessage
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			continue
		}
		p.activity.Store(time.Now().UnixNano())
		if msg.Method == "ui/notifications/initialized" {
			p.once.Do(func() { close(p.ready) })
			continue
		}
		if msg.Method == "" {
			select {
			case p.replies <- msg:
			default:
			}
			continue
		}
		if len(msg.ID) == 0 {
			continue
		}
		// Bound callbacks from the remote frame.
		if p.busy.Add(1) > 16 {
			p.busy.Add(-1)
			_ = p.write(wireMessage{JSONRPC: "2.0", ID: msg.ID, Error: &wireError{-32000, "too many visualization requests"}})
			continue
		}
		callbacks.Add(1)
		go func() {
			defer callbacks.Done()
			defer func() {
				p.busy.Add(-1)
				p.activity.Store(time.Now().UnixNano())
			}()
			p.callback(ctx, client, msg)
		}()
	}
	p.cancel()
	_ = cmd.Wait()
	callbacks.Wait()
}

func (p *process) waitReady(ctx context.Context) error {
	timer := time.NewTimer(45 * time.Second)
	defer timer.Stop()
	select {
	case <-p.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return errors.New("viewer exited before initialization")
	case <-timer.C:
		return errors.New("viewer initialization timed out")
	}
}

func (p *process) write(value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(raw) > 32<<20 {
		return errors.New("viewer message too large")
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err = p.input.Write(append(raw, '\n'))
	return err
}

func (p *process) callback(ctx context.Context, client MCPClient, msg wireMessage) {
	response := wireMessage{JSONRPC: "2.0", ID: msg.ID}
	var err error
	switch msg.Method {
	case "ui/update-model-context":
		response.Result = json.RawMessage(`{}`)
	case "tools/call":
		var args struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		err = json.Unmarshal(msg.Params, &args)
		if err == nil {
			switch args.Name {
			case "_dd_gff", "_dd_guc", "_dd_wfd", "get_widget", "get_widget_v2":
				callCtx, cancel := context.WithTimeout(ctx, time.Minute)
				var result *mcp.CallToolResult
				result, err = client.CallTool(callCtx, &mcp.CallToolParams{Name: args.Name, Arguments: args.Arguments})
				if err == nil {
					if result == nil {
						err = errors.New("missing MCP result")
					} else {
						if result.IsError {
							p.failed.Store(true)
						}
						response.Result, err = json.Marshal(result)
					}
				}
				cancel()
			default:
				err = errors.New("unsupported visualization tool")
			}
		}
	default:
		err = errors.New("unsupported visualization method")
	}
	if err != nil {
		p.failed.Store(true)
		response.Error = &wireError{-32000, "visualization tool request failed"}
		response.Result = nil
	}
	if response.Error == nil && len(response.Result) == 0 {
		response.Result = json.RawMessage(`{}`)
	}
	_ = p.write(response)
}

func (p *process) present(result *mcp.CallToolResult) error {
	p.failed.Store(false)
	p.activity.Store(time.Now().UnixNano())
	return p.write(map[string]any{
		"jsonrpc": "2.0",
		"method":  "ui/notifications/tool-result",
		"params":  result,
	})
}

func (p *process) show() error { return p.write(map[string]string{"command": "show"}) }

func (p *process) snapshot(ctx context.Context) ([]byte, error) {
	// The viewer has no render-complete event; RPC inactivity is a heuristic.
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for p.busy.Load() != 0 || time.Since(time.Unix(0, p.activity.Load())) < 3*time.Second {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-p.done:
			return nil, errors.New("viewer exited")
		case <-ticker.C:
		}
	}
	if p.failed.Load() {
		return nil, errors.New("visualization data request failed")
	}
	id := json.RawMessage(fmt.Sprintf(`"snapshot-%d"`, time.Now().UnixNano()))
	if err := p.write(wireMessage{JSONRPC: "2.0", ID: id, Method: "snapshot"}); err != nil {
		return nil, err
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-p.done:
			return nil, errors.New("viewer exited")
		case <-timer.C:
			return nil, errors.New("snapshot timed out")
		case msg := <-p.replies:
			if string(msg.ID) != string(id) {
				continue
			}
			if msg.Error != nil {
				return nil, errors.New("viewer snapshot failed")
			}
			var result struct {
				Data     string `json:"data"`
				MimeType string `json:"mimeType"`
			}
			if err := json.Unmarshal(msg.Result, &result); err != nil {
				return nil, err
			}
			if result.MimeType != "image/png" {
				return nil, errors.New("viewer returned a non-PNG image")
			}
			return base64.StdEncoding.DecodeString(result.Data)
		}
	}
}

func (p *process) close() {
	_ = p.input.Close()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		_ = p.kill()
		p.cancel()
		<-p.done
	}
}

func (p *process) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

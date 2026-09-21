// Package codex integrates the official Codex app-server over private stdio.
// No app-server port or raw JSON-RPC endpoint is exposed to website visitors.
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type Status struct {
	Connected bool   `json:"connected"`
	Email     string `json:"email,omitempty"`
	Plan      string `json:"plan,omitempty"`
	Login     *Login `json:"login,omitempty"`
	Error     string `json:"error,omitempty"`
}
type Login struct {
	ID   string `json:"loginId"`
	URL  string `json:"verificationUrl"`
	Code string `json:"userCode"`
}
type message struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}
type Client struct {
	home, work, binary, model string
	lifetime                  context.Context
	op                        sync.Mutex
	mu                        sync.Mutex
	input                     io.WriteCloser
	cmd                       *exec.Cmd
	done                      chan struct{}
	pending                   map[int]chan message
	next                      int
	events                    chan message
	status                    Status
	loginStarted              time.Time
}

func New(ctx context.Context, root, binary, model string) (*Client, error) {
	home, work := filepath.Join(root, "credentials"), filepath.Join(root, "workspace")
	for _, dir := range []string{root, home, work} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		if err := os.Chmod(dir, 0700); err != nil {
			return nil, err
		}
	}
	// This is a dedicated, app-owned configuration, never the host's Codex home.
	config := `cli_auth_credentials_store = "file"
web_search = "disabled"
project_doc_max_bytes = 0
[features]
shell_tool = false
multi_agent = false
multi_agent_v2 = false
apps = false
plugins = false
code_mode = false
code_mode_host = false
`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		return nil, err
	}
	return &Client{home: home, work: work, binary: binary, model: model, lifetime: ctx}, nil
}
func (c *Client) start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.cmd != nil {
		select {
		case <-c.done:
			c.cmd = nil
		default:
			c.mu.Unlock()
			return nil
		}
	}
	c.mu.Unlock()
	cmd := exec.CommandContext(c.lifetime, c.binary, "app-server", "--listen", "stdio://")
	cmd.Dir = c.work
	// Explicit allowlist: no website secrets, API keys, host HOME or host Codex auth.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Dir(c.home), "CODEX_HOME=" + c.home, "TMPDIR=" + c.work}
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("Codex runtime unavailable: %w", err)
	}
	done := make(chan struct{})
	c.mu.Lock()
	c.cmd = cmd
	c.input = input
	c.done = done
	c.pending = map[int]chan message{}
	c.events = nil
	c.status = Status{}
	c.mu.Unlock()
	go c.read(output, cmd, done)
	var result any
	if err = c.call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "orchestrator_web", "title": "Orchestrator", "version": "0.1.0"}}, &result); err != nil {
		c.stop()
		return err
	}
	return c.send(map[string]any{"method": "initialized", "params": map[string]any{}})
}
func (c *Client) read(output io.Reader, cmd *exec.Cmd, done chan struct{}) {
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); close(done) }()
	scan := bufio.NewScanner(output)
	scan.Buffer(make([]byte, 4096), 4<<20)
	for scan.Scan() {
		var m message
		if json.Unmarshal(scan.Bytes(), &m) != nil {
			continue
		}
		if len(m.ID) > 0 && m.Method != "" {
			// Fail closed for every server-initiated tool/approval request.
			_ = c.send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "Interactive tools are disabled in the website planner"}})
			continue
		}
		c.mu.Lock()
		if len(m.ID) > 0 {
			var id int
			if json.Unmarshal(m.ID, &id) == nil {
				if ch := c.pending[id]; ch != nil {
					select {
					case ch <- m:
					default:
					}
				}
			}
		}
		if m.Method == "account/login/completed" {
			var p struct {
				Success bool `json:"success"`
			}
			_ = json.Unmarshal(m.Params, &p)
			c.status.Login = nil
			if !p.Success {
				c.status.Error = "ChatGPT sign-in was not completed. Try connecting again."
			}
		}
		if c.events != nil && (m.Method == "item/completed" || m.Method == "turn/completed") {
			select {
			case c.events <- m:
			default:
				c.mu.Unlock()
				return
			}
		}
		c.mu.Unlock()
	}
}
func (c *Client) send(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.input == nil {
		return errors.New("Codex is not running")
	}
	return json.NewEncoder(c.input).Encode(v)
}
func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	c.mu.Lock()
	c.next++
	id := c.next
	ch := make(chan message, 1)
	c.pending[id] = ch
	done := c.done
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err := c.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return errors.New("Codex connection ended; retry the request")
	case m := <-ch:
		if len(m.Error) > 0 && string(m.Error) != "null" {
			return fmt.Errorf("Codex rejected %s", method)
		}
		if result != nil {
			return json.Unmarshal(m.Result, result)
		}
		return nil
	}
}
func (c *Client) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil {
		_ = c.cmd.Process.Kill()
	}
}
func (c *Client) Close() { c.op.Lock(); defer c.op.Unlock(); c.stop() }
func (c *Client) account(ctx context.Context) (Status, error) {
	var result struct {
		Account *struct{ Type, Email, PlanType string } `json:"account"`
	}
	if err := c.call(ctx, "account/read", map[string]any{"refreshToken": false}, &result); err != nil {
		return Status{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Connected = result.Account != nil && result.Account.Type == "chatgpt"
	c.status.Email = ""
	c.status.Plan = ""
	if c.status.Connected {
		c.status.Email = result.Account.Email
		c.status.Plan = result.Account.PlanType
		c.status.Login = nil
		c.status.Error = ""
	}
	if c.status.Login != nil && time.Since(c.loginStarted) > 15*time.Minute {
		c.status.Login = nil
		c.status.Error = "ChatGPT sign-in expired. Try again."
	}
	return c.status, nil
}
func (c *Client) Account(ctx context.Context) (Status, error) {
	c.op.Lock()
	defer c.op.Unlock()
	if err := c.start(ctx); err != nil {
		return Status{}, err
	}
	return c.account(ctx)
}
func (c *Client) Connect(ctx context.Context) (Status, error) {
	c.op.Lock()
	defer c.op.Unlock()
	if err := c.start(ctx); err != nil {
		return Status{}, err
	}
	s, err := c.account(ctx)
	if err != nil || s.Connected || s.Login != nil {
		return s, err
	}
	var login Login
	if err = c.call(ctx, "account/login/start", map[string]string{"type": "chatgptDeviceCode"}, &login); err != nil {
		return s, err
	}
	if login.URL != "https://auth.openai.com/codex/device" || login.ID == "" || login.Code == "" {
		return s, errors.New("unexpected ChatGPT sign-in response")
	}
	c.mu.Lock()
	c.status.Login = &login
	c.status.Error = ""
	c.loginStarted = time.Now()
	s = c.status
	c.mu.Unlock()
	return s, nil
}
func (c *Client) Disconnect(ctx context.Context) error {
	c.op.Lock()
	defer c.op.Unlock()
	if err := c.start(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	login := c.status.Login
	c.mu.Unlock()
	if login != nil {
		if err := c.call(ctx, "account/login/cancel", map[string]string{"loginId": login.ID}, nil); err != nil {
			return err
		}
	}
	if err := c.call(ctx, "account/logout", nil, nil); err != nil {
		return err
	}
	c.mu.Lock()
	c.status = Status{}
	c.mu.Unlock()
	return nil
}

// Generate asks Codex for a structured plan. Restricted reads and disabled
// network/tools keep untrusted prompts away from server files and credentials.
func (c *Client) Generate(ctx context.Context, instructions, prompt string, schema map[string]any) (string, error) {
	c.op.Lock()
	defer c.op.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := c.start(ctx); err != nil {
		return "", err
	}
	status, err := c.account(ctx)
	if err != nil {
		return "", err
	}
	if !status.Connected {
		return "", errors.New("connect ChatGPT before creating an AI plan")
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	params := map[string]any{"cwd": c.work, "approvalPolicy": "never", "sandbox": "read-only", "ephemeral": true, "baseInstructions": instructions + "\nReturn only the requested JSON. Do not call tools or inspect files.", "config": map[string]any{"web_search": "disabled"}}
	if c.model != "" {
		params["model"] = c.model
	}
	if err = c.call(ctx, "thread/start", params, &thread); err != nil {
		return "", err
	}
	if thread.Thread.ID == "" {
		return "", errors.New("Codex did not create a thread")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = c.call(cleanup, "thread/unsubscribe", map[string]string{"threadId": thread.Thread.ID}, nil)
	}()
	events := make(chan message, 128)
	c.mu.Lock()
	c.events = events
	done := c.done
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.events = nil; c.mu.Unlock() }()
	turn := map[string]any{"threadId": thread.Thread.ID, "input": []map[string]string{{"type": "text", "text": prompt}}, "approvalPolicy": "never", "sandboxPolicy": map[string]any{"type": "readOnly", "access": map[string]any{"type": "restricted", "includePlatformDefaults": false, "readableRoots": []string{c.work}}}, "outputSchema": schema}
	if err = c.call(ctx, "turn/start", turn, nil); err != nil {
		c.stop()
		return "", err
	}
	var output string
	for {
		select {
		case <-ctx.Done():
			c.stop()
			return "", ctx.Err()
		case <-done:
			return "", errors.New("Codex connection ended")
		case event := <-events:
			var p struct {
				ThreadID string                      `json:"threadId"`
				Item     struct{ Type, Text string } `json:"item"`
				Turn     struct{ Status string }     `json:"turn"`
			}
			if json.Unmarshal(event.Params, &p) != nil || p.ThreadID != thread.Thread.ID {
				continue
			}
			if event.Method == "item/completed" && p.Item.Type == "agentMessage" {
				output = p.Item.Text
			}
			if event.Method == "turn/completed" {
				if p.Turn.Status != "completed" || output == "" {
					return "", errors.New("Codex could not complete the plan; check your ChatGPT connection and usage limits")
				}
				return output, nil
			}
		}
	}
}

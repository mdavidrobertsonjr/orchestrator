package codex

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orchestrator/backend/internal/llm"
)

func TestConnectionAndStructuredPlan(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "fake-codex")
	// A deterministic protocol peer exercises real process IO and notification
	// ordering without connecting an account or spending model tokens.
	source := `#!/usr/bin/env python3
import json,sys,os
connected=False
for line in sys.stdin:
 m=json.loads(line); method=m.get('method'); result={}
 if method=='initialize':
  assert os.environ.get('OPENAI_API_KEY') is None
  assert os.environ.get('ORCH_GOOGLE_CLIENT_SECRET') is None
 elif method=='account/read': result={'account':{'type':'chatgpt','email':'person@example.com','planType':'plus'} if connected else None}
 elif method=='account/login/start':
  assert m['params']['type']=='chatgptDeviceCode'
  result={'loginId':'login','verificationUrl':'https://auth.openai.com/codex/device','userCode':'ABCD-1234'}
  connected=True
 elif method=='account/logout': connected=False
 elif method=='thread/start':
  assert m['params']['ephemeral'] is True
  assert 'sandbox' not in m['params']
  config=open(os.path.join(os.environ['CODEX_HOME'],'config.toml')).read()
  assert 'default_permissions = "planner"' in config
  assert '":root" = "deny"' in config
  assert '[permissions.planner.filesystem.":workspace_roots"]' in config
  assert '"." = "read"' in config
  assert '[permissions.planner.network]\nenabled = false' in config
  result={'thread':{'id':'thread'}}
 elif method=='turn/start':
  p=m['params'];assert 'sandboxPolicy' not in p
  assert p['approvalPolicy']=='never'
  assert p['outputSchema']['type']=='object'
 if 'id' in m:
  print(json.dumps({'id':m['id'],'result':result}),flush=True)
 if method=='turn/start':
  print(json.dumps({'method':'item/completed','params':{'threadId':'thread','item':{'type':'agentMessage','text':'{"answer":"planned"}'}}}),flush=True)
  print(json.dumps({'method':'turn/completed','params':{'threadId':'thread','turn':{'status':'completed'}}}),flush=True)
`
	if err := os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "host-key-must-not-leak")
	t.Setenv("ORCH_GOOGLE_CLIENT_SECRET", "host-secret")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := New(ctx, filepath.Join(root, "user"), script, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s, err := c.Account(ctx)
	if err != nil || s.Connected {
		t.Fatal(s, err)
	}
	if _, err = c.Generate(ctx, "instructions", "hello", map[string]any{"type": "object"}); err == nil {
		t.Fatal("planned without connection")
	}
	s, err = c.Connect(ctx)
	if err != nil || s.Login == nil || s.Login.Code != "ABCD-1234" {
		t.Fatal(s, err)
	}
	s, err = c.Account(ctx)
	if err != nil || !s.Connected || s.Login != nil {
		t.Fatal(s, err)
	}
	output, err := c.Generate(ctx, "instructions", "hello", map[string]any{"type": "object"})
	if err != nil || !json.Valid([]byte(output)) {
		t.Fatal(output, err)
	}
	if err = c.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	s, err = c.Account(ctx)
	if err != nil || s.Connected {
		t.Fatal(s, err)
	}
}
func TestRealCodexHandshake(t *testing.T) {
	if os.Getenv("ORCH_TEST_CODEX") != "1" {
		t.Skip("set ORCH_TEST_CODEX=1 to verify installed app-server protocol")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := New(ctx, t.TempDir(), binary, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	status, err := c.Account(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Connected {
		t.Fatal("fresh isolated directory borrowed host credentials")
	}
	data, _ := os.ReadFile(filepath.Join(c.home, "config.toml"))
	if !strings.Contains(string(data), "shell_tool = false") {
		t.Fatal("missing tool restriction")
	}
}

// This opt-in check uses an app-owned connected account and spends model usage,
// but only generates a plan; it never queues jobs or persists a workflow.
func TestRealConnectedCommandPlan(t *testing.T) {
	root := os.Getenv("ORCH_TEST_CODEX_USER_ROOT")
	if root == "" {
		t.Skip("set ORCH_TEST_CODEX_USER_ROOT to an app-owned connected account directory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	c, err := New(ctx, root, "codex", "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	plan, err := llm.NewConnectedPlanner(c.Generate).PlanCommand(ctx, "Monitor new-grad software engineering roles at OpenAI, Palantir, Anduril, and SpaceX every hour")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Action != "workflow" || plan.Workflow.JobType != "jobs.monitor.new_grad" || plan.Workflow.IntervalSeconds != 3600 {
		t.Fatalf("unexpected command plan: %#v", plan)
	}
	sources, ok := plan.Workflow.Payload["sources"].([]any)
	if !ok || len(sources) != 4 {
		t.Fatalf("expected four monitoring sources, got %#v", plan.Workflow.Payload["sources"])
	}
	t.Log("Generated hourly workflow plan with all four monitoring sources; no workflow created")
}

package email

import (
	"errors"
	"net/smtp"
	"strings"
	"testing"
)

func TestSimulatedSenderLogsMessageDetails(t *testing.T) {
	var logs []string
	sender := NewSimulatedSender(func(message string) {
		logs = append(logs, message)
	})

	err := sender.Send(t.Context(), Message{
		Recipients: []string{"ops@example.com"},
		Subject:    "Daily failure report",
		Metadata: map[string]string{
			"kind":     "failure_alert",
			"schedule": "daily at 8am",
		},
	})
	if err != nil {
		t.Fatalf("send simulated email: %v", err)
	}

	if !containsLog(logs, "email report recipients: ops@example.com") {
		t.Fatalf("expected recipients log, got %#v", logs)
	}
	if !containsLog(logs, "email report subject: Daily failure report") {
		t.Fatalf("expected subject log, got %#v", logs)
	}
	if !containsLog(logs, "email report kind: failure_alert") {
		t.Fatalf("expected kind log, got %#v", logs)
	}
	if !containsLog(logs, "email report schedule noted but not yet scheduled: daily at 8am") {
		t.Fatalf("expected schedule log, got %#v", logs)
	}
}

func containsLog(logs []string, message string) bool {
	for _, log := range logs {
		if log == message {
			return true
		}
	}
	return false
}

func TestSMTPSenderSendsFormattedMessage(t *testing.T) {
	var gotAddr string
	var gotFrom string
	var gotTo []string
	var gotMessage string

	sender := NewSMTPSender(SMTPConfig{
		Host: "smtp.example.com",
		Port: 587,
		From: "orchestrator@example.com",
	})
	sender.send = func(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
		gotAddr = addr
		gotFrom = from
		gotTo = append([]string(nil), to...)
		gotMessage = string(msg)
		if auth != nil {
			t.Fatal("expected nil auth without username/password")
		}
		return nil
	}

	err := sender.Send(t.Context(), Message{
		Recipients: []string{"ops@example.com"},
		Subject:    "Daily report",
		Body:       "All systems nominal.",
	})
	if err != nil {
		t.Fatalf("send smtp email: %v", err)
	}

	if gotAddr != "smtp.example.com:587" {
		t.Fatalf("unexpected addr %q", gotAddr)
	}
	if gotFrom != "orchestrator@example.com" {
		t.Fatalf("unexpected from %q", gotFrom)
	}
	if len(gotTo) != 1 || gotTo[0] != "ops@example.com" {
		t.Fatalf("unexpected recipients %#v", gotTo)
	}
	for _, expected := range []string{
		"From: orchestrator@example.com",
		"To: ops@example.com",
		"Subject: Daily report",
		"Content-Type: text/plain",
		"All systems nominal.",
	} {
		if !strings.Contains(gotMessage, expected) {
			t.Fatalf("expected message to contain %q, got %q", expected, gotMessage)
		}
	}
}

func TestSMTPSenderValidatesRequiredConfig(t *testing.T) {
	tests := []struct {
		name   string
		config SMTPConfig
		msg    Message
	}{
		{name: "missing host", config: SMTPConfig{Port: 587, From: "from@example.com"}, msg: Message{Recipients: []string{"to@example.com"}}},
		{name: "missing port", config: SMTPConfig{Host: "smtp.example.com", From: "from@example.com"}, msg: Message{Recipients: []string{"to@example.com"}}},
		{name: "missing from", config: SMTPConfig{Host: "smtp.example.com", Port: 587}, msg: Message{Recipients: []string{"to@example.com"}}},
		{name: "missing recipients", config: SMTPConfig{Host: "smtp.example.com", Port: 587, From: "from@example.com"}, msg: Message{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender := NewSMTPSender(tt.config)
			sender.send = func(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
				return errors.New("send should not be called")
			}

			if err := sender.Send(t.Context(), tt.msg); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

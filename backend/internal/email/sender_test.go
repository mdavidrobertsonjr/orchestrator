package email

import "testing"

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

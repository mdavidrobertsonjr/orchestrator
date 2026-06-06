package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"orchestrator/backend/internal/email"
	"orchestrator/backend/internal/jobs"
	"orchestrator/backend/internal/monitor"
	"orchestrator/backend/internal/results"
)

type Executor interface {
	Execute(ctx context.Context, job *jobs.Job, logf func(string)) error
}

type SimulatedExecutor struct {
	logger            *slog.Logger
	monitorRunner     *monitor.Runner
	results           results.Store
	emailSender       email.Sender
	defaultRecipients []string
}

func NewSimulatedExecutor(logger *slog.Logger, monitorRunner *monitor.Runner, resultStore results.Store, emailSender email.Sender) *SimulatedExecutor {
	return &SimulatedExecutor{logger: logger, monitorRunner: monitorRunner, results: resultStore, emailSender: emailSender}
}

func (e *SimulatedExecutor) SetDefaultRecipients(recipients []string) {
	e.defaultRecipients = cleanRecipients(recipients)
}

func (e *SimulatedExecutor) Execute(ctx context.Context, job *jobs.Job, logf func(string)) error {
	duration := durationFromPayload(job.Payload)

	logf(fmt.Sprintf("executor received %q job", job.Type))
	if job.Type == monitor.NewGradJobType {
		result, err := e.monitorRunner.Run(ctx, job.Payload, logf)
		if err != nil {
			return err
		}
		alertSent, err := e.sendMonitorAlert(ctx, job, result, logf)
		if err != nil {
			return err
		}
		if err := e.recordMonitorResult(job, result, alertSent, logf); err != nil {
			return err
		}
	} else if job.Type == "report.email" {
		sender := e.emailSender
		if sender == nil {
			sender = email.NewSimulatedSender(logf)
		}
		if err := e.sendEmailReport(ctx, job.Payload, sender, logf); err != nil {
			return err
		}
	}
	logf(fmt.Sprintf("simulating work for %s", duration))

	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}

	if shouldFail(job.Payload) {
		return errors.New("simulated job failure")
	}

	logf("executor completed work")
	return nil
}

func (e *SimulatedExecutor) sendMonitorAlert(ctx context.Context, job *jobs.Job, result *monitor.Result, logf func(string)) (bool, error) {
	if result == nil || result.Created == 0 {
		return false, nil
	}

	config := monitorNotificationConfig(job.Payload)
	if config.mode != "immediate" {
		return false, nil
	}
	if len(config.recipients) == 0 {
		config.recipients = e.defaultRecipients
	}
	if len(config.recipients) == 0 {
		logf("monitor alert skipped; no recipients configured")
		return false, nil
	}

	sender := e.emailSender
	if sender == nil {
		sender = email.NewSimulatedSender(logf)
	}

	if err := sender.Send(ctx, email.Message{
		Recipients: config.recipients,
		Subject:    fmt.Sprintf("New job monitor matches: %d", result.Created),
		Body:       monitorAlertBody(job, result),
		Metadata: map[string]string{
			"kind": "monitor_alert",
		},
	}); err != nil {
		return false, err
	}

	logf(fmt.Sprintf("monitor alert sent to %s", strings.Join(config.recipients, ", ")))
	return true, nil
}

type notificationConfig struct {
	mode       string
	recipients []string
}

func monitorNotificationConfig(payload map[string]any) notificationConfig {
	var parsed struct {
		NotificationMode string   `json:"notification_mode"`
		Recipients       []string `json:"recipients"`
		Notifications    struct {
			Mode       string   `json:"mode"`
			Recipients []string `json:"recipients"`
		} `json:"notifications"`
	}

	data, err := json.Marshal(payload)
	if err == nil {
		_ = json.Unmarshal(data, &parsed)
	}

	mode := strings.ToLower(strings.TrimSpace(parsed.Notifications.Mode))
	if mode == "" {
		mode = strings.ToLower(strings.TrimSpace(parsed.NotificationMode))
	}
	recipients := parsed.Notifications.Recipients
	if len(recipients) == 0 {
		recipients = parsed.Recipients
	}
	return notificationConfig{mode: mode, recipients: cleanRecipients(recipients)}
}

func cleanRecipients(recipients []string) []string {
	var out []string
	for _, recipient := range recipients {
		recipient = strings.TrimSpace(recipient)
		if recipient != "" {
			out = append(out, recipient)
		}
	}
	return out
}

func monitorAlertBody(job *jobs.Job, result *monitor.Result) string {
	return fmt.Sprintf("Workflow monitor found new postings.\n\nJob: %s\nScanned: %d\nMatched: %d\nNew: %d\nUpdated: %d\n",
		job.ID,
		result.Scanned,
		result.Matched,
		result.Created,
		result.Updated,
	)
}

func (e *SimulatedExecutor) recordMonitorResult(job *jobs.Job, result *monitor.Result, alertSent bool, logf func(string)) error {
	if e.results == nil || result == nil {
		return nil
	}

	workflowID := ""
	if job.Metadata != nil {
		workflowID = job.Metadata["workflow_id"]
	}

	created, err := e.results.Create(results.CreateResultParams{
		JobID:      job.ID,
		WorkflowID: workflowID,
		Type:       "monitor.summary",
		Summary:    fmt.Sprintf("scanned %d postings, matched %d, new %d", result.Scanned, result.Matched, result.Created),
		Data: map[string]any{
			"scanned":    result.Scanned,
			"matched":    result.Matched,
			"created":    result.Created,
			"updated":    result.Updated,
			"alert_sent": alertSent,
		},
	})
	if err != nil {
		return err
	}

	logf("recorded monitor result: " + created.ID)
	return nil
}

func (e *SimulatedExecutor) sendEmailReport(ctx context.Context, payload map[string]any, sender email.Sender, logf func(string)) error {
	report, ok := payload["report"]
	if !ok {
		logf("email report payload missing; using default report")
		return sender.Send(ctx, email.Message{
			Subject:  "Orchestrator report",
			Metadata: map[string]string{"kind": "job_summary", "schedule": "immediate"},
		})
	}

	data, err := json.Marshal(report)
	if err != nil {
		logf("email report payload could not be encoded")
		return err
	}

	var parsed struct {
		Recipients []string `json:"recipients"`
		Subject    string   `json:"subject"`
		Kind       string   `json:"kind"`
		Schedule   string   `json:"schedule"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		logf("email report payload could not be decoded")
		return err
	}

	body := reportBody(parsed.Kind)
	if parsed.Kind == "monitor_digest" {
		body = e.monitorDigestBody(logf)
	}

	return sender.Send(ctx, email.Message{
		Recipients: parsed.Recipients,
		Subject:    parsed.Subject,
		Body:       body,
		Metadata: map[string]string{
			"kind":     parsed.Kind,
			"schedule": parsed.Schedule,
		},
	})
}

func (e *SimulatedExecutor) monitorDigestBody(logf func(string)) string {
	if e.results == nil {
		logf("monitor digest requested but result store is not configured")
		return "Monitor digest\n\nNo result store is configured.\n"
	}

	stored, err := e.results.List()
	if err != nil {
		logf("monitor digest could not list results: " + err.Error())
		return "Monitor digest\n\nFailed to load monitor results.\n"
	}

	var out strings.Builder
	out.WriteString("Monitor digest\n\n")
	count := 0
	for _, result := range stored {
		if result.Type != "monitor.summary" {
			continue
		}
		count++
		out.WriteString("- ")
		out.WriteString(result.Summary)
		if result.WorkflowID != "" {
			out.WriteString(" (workflow ")
			out.WriteString(result.WorkflowID)
			out.WriteString(")")
		}
		out.WriteString("\n")
		if count == 10 {
			break
		}
	}
	if count == 0 {
		out.WriteString("No monitor results recorded yet.\n")
	}
	return out.String()
}

func reportBody(kind string) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "job_summary"
	}
	return "Orchestrator report\n\nKind: " + kind + "\n"
}

func durationFromPayload(payload map[string]any) time.Duration {
	raw, ok := payload["duration_ms"]
	if !ok {
		return 1500 * time.Millisecond
	}

	switch value := raw.(type) {
	case float64:
		return boundedDuration(time.Duration(value) * time.Millisecond)
	case int:
		return boundedDuration(time.Duration(value) * time.Millisecond)
	default:
		return 1500 * time.Millisecond
	}
}

func boundedDuration(duration time.Duration) time.Duration {
	if duration < 100*time.Millisecond {
		return 100 * time.Millisecond
	}
	if duration > 30*time.Second {
		return 30 * time.Second
	}
	return duration
}

func shouldFail(payload map[string]any) bool {
	raw, ok := payload["should_fail"]
	if !ok {
		return false
	}

	value, ok := raw.(bool)
	return ok && value
}

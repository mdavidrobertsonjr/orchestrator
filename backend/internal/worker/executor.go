package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"orchestrator/backend/internal/email"
	"orchestrator/backend/internal/jobs"
	"orchestrator/backend/internal/monitor"
	"orchestrator/backend/internal/notifications"
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
	notifications     notifications.Store
	defaultRecipients []string
	allowedRecipients []string
	httpClient        *http.Client
}

func NewSimulatedExecutor(logger *slog.Logger, monitorRunner *monitor.Runner, resultStore results.Store, emailSender email.Sender) *SimulatedExecutor {
	return &SimulatedExecutor{logger: logger, monitorRunner: monitorRunner, results: resultStore, emailSender: emailSender}
}

func (e *SimulatedExecutor) SetDefaultRecipients(recipients []string) {
	e.defaultRecipients = cleanRecipients(recipients)
}

// SetAllowedRecipients constrains all hosted deliveries to verified account
// addresses. Private deployments leave this unset and retain their existing
// recipient behavior.
func (e *SimulatedExecutor) SetAllowedRecipients(recipients []string) {
	e.allowedRecipients = cleanRecipients(recipients)
}

func (e *SimulatedExecutor) SetNotificationStore(store notifications.Store) {
	e.notifications = store
}

func (e *SimulatedExecutor) SetHTTPClient(client *http.Client) {
	e.httpClient = client
}

func (e *SimulatedExecutor) Execute(ctx context.Context, job *jobs.Job, logf func(string)) error {
	duration := durationFromPayload(job.Payload)

	logf(fmt.Sprintf("executor received %q job", job.Type))
	if job.Type == monitor.NewGradJobType {
		result, err := e.monitorRunner.Run(ctx, job.Payload, logf)
		if err != nil {
			_ = e.recordMonitorSourceFailure(job, err, logf)
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
		if err := e.sendEmailReport(ctx, job, sender, logf); err != nil {
			return err
		}
	} else if job.Type == "http.request" {
		if err := e.executeHTTPRequest(ctx, job, logf); err != nil {
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

func (e *SimulatedExecutor) executeHTTPRequest(ctx context.Context, job *jobs.Job, logf func(string)) error {
	var payload struct {
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
		Timeout int               `json:"timeout_ms"`
	}
	data, err := json.Marshal(job.Payload)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}

	payload.Method = strings.ToUpper(strings.TrimSpace(payload.Method))
	if payload.Method == "" {
		payload.Method = http.MethodGet
	}
	payload.URL = strings.TrimSpace(payload.URL)
	if payload.URL == "" {
		return errors.New("http.request payload requires url")
	}

	timeout := time.Duration(payload.Timeout) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	client := e.httpClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	} else if client.Timeout == 0 {
		copy := *client
		copy.Timeout = timeout
		client = &copy
	}
	req, err := http.NewRequestWithContext(ctx, payload.Method, payload.URL, bytes.NewBufferString(payload.Body))
	if err != nil {
		return err
	}
	for key, value := range payload.Headers {
		req.Header.Set(key, value)
	}

	logf(fmt.Sprintf("http request started: %s %s", payload.Method, payload.URL))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return err
	}
	logf(fmt.Sprintf("http request completed with status %d", resp.StatusCode))

	if e.results != nil {
		workflowID := ""
		if job.Metadata != nil {
			workflowID = job.Metadata["workflow_id"]
		}
		if _, err := e.results.Create(results.CreateResultParams{
			JobID:      job.ID,
			WorkflowID: workflowID,
			Type:       "http.response",
			Summary:    fmt.Sprintf("%s %s returned %d", payload.Method, payload.URL, resp.StatusCode),
			Data: map[string]any{
				"method":      payload.Method,
				"url":         payload.URL,
				"status_code": resp.StatusCode,
				"body_bytes":  len(body),
			},
		}); err != nil {
			return err
		}
	}

	if resp.StatusCode >= 500 {
		return fmt.Errorf("http request returned retryable status %d", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("http request returned status %d", resp.StatusCode)
	}
	return nil
}

func (e *SimulatedExecutor) sendMonitorAlert(ctx context.Context, job *jobs.Job, result *monitor.Result, logf func(string)) (bool, error) {
	if result == nil || result.Created == 0 {
		return false, nil
	}

	config := monitorNotificationConfig(job.Payload)
	if config.mode == "digest" || config.mode == "digest_only" {
		logf("monitor alert skipped; digest-only notifications configured")
		return false, nil
	}
	if config.mode != "immediate" {
		return false, nil
	}
	if isQuietHour(config, time.Now()) {
		logf("monitor alert skipped; quiet hours are active")
		return false, nil
	}
	if e.maxAlertsReached(job, config) {
		logf("monitor alert skipped; max alerts reached for workflow")
		return false, nil
	}
	if len(config.recipients) == 0 {
		config.recipients = e.defaultRecipients
	}
	config.recipients = e.filterAllowedRecipients(config.recipients)
	if len(config.recipients) == 0 {
		logf("monitor alert skipped; no recipients configured")
		return false, nil
	}

	sender := e.emailSender
	if sender == nil {
		sender = email.NewSimulatedSender(logf)
	}

	message := email.Message{
		Recipients: config.recipients,
		Subject:    monitorAlertSubject(result),
		Body:       monitorAlertBody(job, result),
		Metadata: map[string]string{
			"kind": "monitor_alert",
		},
	}
	if err := e.sendTrackedEmail(ctx, job, sender, message, logf); err != nil {
		return false, err
	}

	logf(fmt.Sprintf("monitor alert sent to %s", strings.Join(config.recipients, ", ")))
	return true, nil
}

type notificationConfig struct {
	mode                 string
	recipients           []string
	quietHoursStart      string
	quietHoursEnd        string
	timezone             string
	maxAlertsPerWorkflow int
}

func monitorNotificationConfig(payload map[string]any) notificationConfig {
	var parsed struct {
		NotificationMode string   `json:"notification_mode"`
		Recipients       []string `json:"recipients"`
		Notifications    struct {
			Mode                 string   `json:"mode"`
			Recipients           []string `json:"recipients"`
			QuietHoursStart      string   `json:"quiet_hours_start"`
			QuietHoursEnd        string   `json:"quiet_hours_end"`
			Timezone             string   `json:"timezone"`
			MaxAlertsPerWorkflow int      `json:"max_alerts_per_workflow"`
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
	return notificationConfig{
		mode:                 mode,
		recipients:           cleanRecipients(recipients),
		quietHoursStart:      parsed.Notifications.QuietHoursStart,
		quietHoursEnd:        parsed.Notifications.QuietHoursEnd,
		timezone:             parsed.Notifications.Timezone,
		maxAlertsPerWorkflow: parsed.Notifications.MaxAlertsPerWorkflow,
	}
}

func isQuietHour(config notificationConfig, now time.Time) bool {
	if strings.TrimSpace(config.quietHoursStart) == "" || strings.TrimSpace(config.quietHoursEnd) == "" {
		return false
	}
	location := time.Local
	if strings.TrimSpace(config.timezone) != "" {
		if loaded, err := time.LoadLocation(config.timezone); err == nil {
			location = loaded
		}
	}
	localNow := now.In(location)
	start, ok := parseClock(config.quietHoursStart, localNow)
	if !ok {
		return false
	}
	end, ok := parseClock(config.quietHoursEnd, localNow)
	if !ok {
		return false
	}
	if start.Equal(end) {
		return false
	}
	if start.Before(end) {
		return !localNow.Before(start) && localNow.Before(end)
	}
	return !localNow.Before(start) || localNow.Before(end)
}

func parseClock(raw string, base time.Time) (time.Time, bool) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(base.Year(), base.Month(), base.Day(), parsed.Hour(), parsed.Minute(), 0, 0, base.Location()), true
}

func (e *SimulatedExecutor) maxAlertsReached(job *jobs.Job, config notificationConfig) bool {
	if config.maxAlertsPerWorkflow <= 0 || e.results == nil || job == nil || job.Metadata == nil {
		return false
	}
	workflowID := strings.TrimSpace(job.Metadata["workflow_id"])
	if workflowID == "" {
		return false
	}
	stored, err := e.results.ListByWorkflow(workflowID)
	if err != nil {
		return false
	}
	sent := 0
	for _, result := range stored {
		if result.Type == "monitor.summary" && result.Data != nil && result.Data["alert_sent"] == true {
			sent++
		}
	}
	return sent >= config.maxAlertsPerWorkflow
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

func monitorAlertSubject(result *monitor.Result) string {
	if result != nil && len(result.NewPostings) == 1 {
		posting := result.NewPostings[0]
		return fmt.Sprintf("New match: %s — %s", posting.Company, posting.Title)
	}
	if result != nil && len(result.NewPostings) > 1 {
		return fmt.Sprintf("%d new job matches — %s and more", len(result.NewPostings), result.NewPostings[0].Company)
	}
	if result == nil {
		return "New job match"
	}
	return fmt.Sprintf("%d new job matches", result.Created)
}

func monitorAlertBody(job *jobs.Job, result *monitor.Result) string {
	var body strings.Builder
	body.WriteString("New matching job postings were discovered.\n\n")
	for index, posting := range result.NewPostings {
		fmt.Fprintf(&body, "%d. %s\n", index+1, posting.Title)
		fmt.Fprintf(&body, "Company: %s\n", posting.Company)
		if strings.TrimSpace(posting.Location) != "" {
			fmt.Fprintf(&body, "Location: %s\n", posting.Location)
		}
		fmt.Fprintf(&body, "Match score: %d\n", posting.MatchScore)
		if len(posting.MatchReasons) > 0 {
			fmt.Fprintf(&body, "Matched because: %s\n", strings.Join(posting.MatchReasons, ", "))
		}
		fmt.Fprintf(&body, "Apply: %s\n\n", posting.URL)
	}
	fmt.Fprintf(&body, "Run summary: scanned %d, matched %d, new %d, updated %d.\n", result.Scanned, result.Matched, result.Created, result.Updated)
	if job != nil {
		fmt.Fprintf(&body, "Job: %s\n", job.ID)
	}
	return body.String()
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
			"scanned":       result.Scanned,
			"matched":       result.Matched,
			"created":       result.Created,
			"updated":       result.Updated,
			"source_errors": result.SourceErrors,
			"alert_sent":    alertSent,
		},
	})
	if err != nil {
		return err
	}

	logf("recorded monitor result: " + created.ID)
	return nil
}

func (e *SimulatedExecutor) recordMonitorSourceFailure(job *jobs.Job, runErr error, logf func(string)) error {
	if e.results == nil || job == nil {
		return nil
	}
	workflowID := ""
	if job.Metadata != nil {
		workflowID = job.Metadata["workflow_id"]
	}
	sourceTypes := monitorSourceTypes(job.Payload)
	created, err := e.results.Create(results.CreateResultParams{
		JobID:      job.ID,
		WorkflowID: workflowID,
		Type:       "monitor.source_health",
		Summary:    "monitor source failure: " + runErr.Error(),
		Data: map[string]any{
			"status":  "failed",
			"error":   runErr.Error(),
			"sources": sourceTypes,
		},
	})
	if err != nil {
		return err
	}
	logf("recorded monitor source health result: " + created.ID)
	return nil
}

func monitorSourceTypes(payload map[string]any) []string {
	var parsed struct {
		Sources []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"sources"`
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	var out []string
	for _, source := range parsed.Sources {
		sourceType := strings.TrimSpace(source.Type)
		if sourceType == "" {
			sourceType = "fake"
		}
		if source.Name != "" {
			sourceType += ":" + strings.TrimSpace(source.Name)
		}
		out = append(out, sourceType)
	}
	return out
}

func (e *SimulatedExecutor) sendEmailReport(ctx context.Context, job *jobs.Job, sender email.Sender, logf func(string)) error {
	payload := job.Payload
	report, ok := payload["report"]
	if !ok {
		logf("email report payload missing; using default report")
		return e.sendTrackedEmail(ctx, job, sender, email.Message{
			Subject:  "Orchestrator report",
			Metadata: map[string]string{"kind": "job_summary", "schedule": "immediate"},
		}, logf)
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
	if len(parsed.Recipients) == 0 {
		parsed.Recipients = e.defaultRecipients
	}
	parsed.Recipients = e.filterAllowedRecipients(parsed.Recipients)

	body := reportBody(parsed.Kind)
	if parsed.Kind == "monitor_digest" {
		body = e.monitorDigestBody(logf)
	}

	return e.sendTrackedEmail(ctx, job, sender, email.Message{
		Recipients: parsed.Recipients,
		Subject:    parsed.Subject,
		Body:       body,
		Metadata: map[string]string{
			"kind":     parsed.Kind,
			"schedule": parsed.Schedule,
		},
	}, logf)
}

func (e *SimulatedExecutor) filterAllowedRecipients(recipients []string) []string {
	if len(e.allowedRecipients) == 0 {
		return cleanRecipients(recipients)
	}
	allowed := make(map[string]struct{}, len(e.allowedRecipients))
	for _, recipient := range e.allowedRecipients {
		allowed[strings.ToLower(recipient)] = struct{}{}
	}
	filtered := make([]string, 0, len(recipients))
	for _, recipient := range cleanRecipients(recipients) {
		if _, ok := allowed[strings.ToLower(recipient)]; ok {
			filtered = append(filtered, recipient)
		}
	}
	return filtered
}

func (e *SimulatedExecutor) sendTrackedEmail(ctx context.Context, job *jobs.Job, sender email.Sender, message email.Message, logf func(string)) error {
	if sender == nil {
		return errors.New("email sender is not configured")
	}

	var deliveryID string
	if e.notifications != nil {
		jobID := ""
		workflowID := ""
		if job != nil {
			jobID = job.ID
			if job.Metadata != nil {
				workflowID = job.Metadata["workflow_id"]
			}
		}
		kind := strings.TrimSpace(message.Metadata["kind"])
		if kind == "" {
			kind = "email"
		}
		delivery, err := e.notifications.Create(notifications.CreateDeliveryParams{
			JobID:      jobID,
			WorkflowID: workflowID,
			Kind:       kind,
			Provider:   "smtp",
			Recipients: message.Recipients,
			Subject:    message.Subject,
			Metadata: map[string]any{
				"schedule": message.Metadata["schedule"],
			},
		})
		if err != nil {
			return err
		}
		deliveryID = delivery.ID
		if _, err := e.notifications.MarkAttempt(deliveryID); err != nil {
			return err
		}
		logf("notification delivery recorded: " + deliveryID)
	}

	if err := sender.Send(ctx, message); err != nil {
		if e.notifications != nil && deliveryID != "" {
			_, _ = e.notifications.MarkFailed(deliveryID, err.Error())
		}
		return err
	}
	if e.notifications != nil && deliveryID != "" {
		if _, err := e.notifications.MarkSucceeded(deliveryID); err != nil {
			return err
		}
	}
	return nil
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

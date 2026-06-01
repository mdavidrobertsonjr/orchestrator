package email

import (
	"context"
	"strings"
)

type Message struct {
	Recipients []string
	Subject    string
	Body       string
	Metadata   map[string]string
}

type Sender interface {
	Send(ctx context.Context, message Message) error
}

type LogFunc func(string)

type SimulatedSender struct {
	logf LogFunc
}

func NewSimulatedSender(logf LogFunc) *SimulatedSender {
	return &SimulatedSender{logf: logf}
}

func (s *SimulatedSender) Send(ctx context.Context, message Message) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if len(message.Recipients) == 0 {
		s.log("email report has no recipients; delivery is simulated")
	} else {
		s.log("email report recipients: " + strings.Join(message.Recipients, ", "))
	}
	if message.Subject != "" {
		s.log("email report subject: " + message.Subject)
	}
	if kind := message.Metadata["kind"]; kind != "" {
		s.log("email report kind: " + kind)
	}
	if schedule := message.Metadata["schedule"]; schedule != "" && schedule != "immediate" {
		s.log("email report schedule noted but not yet scheduled: " + schedule)
	}
	s.log("email delivery provider not configured; simulated report only")

	return nil
}

func (s *SimulatedSender) log(message string) {
	if s.logf != nil {
		s.logf(message)
	}
}

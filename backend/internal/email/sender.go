package email

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/smtp"
	"strconv"
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

type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

type SMTPSender struct {
	config SMTPConfig
	send   func(addr string, auth smtp.Auth, from string, to []string, msg []byte) error
}

func NewSMTPSender(config SMTPConfig) *SMTPSender {
	return &SMTPSender{
		config: config,
		send:   smtp.SendMail,
	}
}

func (s *SMTPSender) Send(ctx context.Context, message Message) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if strings.TrimSpace(s.config.Host) == "" {
		return errors.New("SMTP host is required")
	}
	if s.config.Port <= 0 {
		return errors.New("SMTP port is required")
	}
	if strings.TrimSpace(s.config.From) == "" {
		return errors.New("SMTP from address is required")
	}
	if len(message.Recipients) == 0 {
		return errors.New("email recipients are required")
	}

	addr := s.config.Host + ":" + strconv.Itoa(s.config.Port)
	var auth smtp.Auth
	if s.config.Username != "" || s.config.Password != "" {
		auth = smtp.PlainAuth("", s.config.Username, s.config.Password, s.config.Host)
	}

	return s.send(addr, auth, s.config.From, message.Recipients, formatMessage(s.config.From, message))
}

func formatMessage(from string, message Message) []byte {
	subject := strings.TrimSpace(message.Subject)
	if subject == "" {
		subject = "Orchestrator report"
	}
	body := strings.TrimSpace(message.Body)
	if body == "" {
		body = "No report body was provided."
	}

	var out bytes.Buffer
	writeHeader(&out, "From", from)
	writeHeader(&out, "To", strings.Join(message.Recipients, ", "))
	writeHeader(&out, "Subject", subject)
	writeHeader(&out, "MIME-Version", "1.0")
	writeHeader(&out, "Content-Type", `text/plain; charset="UTF-8"`)
	out.WriteString("\r\n")
	out.WriteString(body)
	out.WriteString("\r\n")
	return out.Bytes()
}

func writeHeader(out *bytes.Buffer, key string, value string) {
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "")
	out.WriteString(fmt.Sprintf("%s: %s\r\n", key, value))
}

package hosted

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"time"

	"orchestrator/backend/internal/email"
)

type passwordAccounts interface {
	BeginEmail(context.Context, string, string) (string, error)
	CompleteEmail(context.Context, string, string, string) (User, string, error)
	PasswordLogin(context.Context, string, string) (User, string, error)
}

func (s *Server) emailAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, 405, "method not allowed")
		return
	}
	accounts, ok := s.accounts.(passwordAccounts)
	if !ok {
		fail(w, 503, "email sign-in unavailable")
		return
	}
	if !s.limits.allow("password-global", 30) {
		fail(w, 429, "too many sign-in requests; try again in a minute")
		return
	}
	var input struct{ Email, Password, Name, Token string }
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		fail(w, 400, "invalid request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if r.URL.Path != "/auth/email/complete" {
		var err error
		input.Email, err = normalizeEmail(input.Email)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		if !s.limits.allow("email:"+tokenHash(input.Email), 5) {
			fail(w, 429, "too many attempts; try again in a minute")
			return
		}
	}
	switch r.URL.Path {
	case "/auth/email/signup", "/auth/email/reset":
		if s.mailer == nil {
			fail(w, 503, "email delivery is not configured; please use Google sign-in")
			return
		}
		purpose := "signup"
		if r.URL.Path == "/auth/email/reset" {
			purpose = "reset"
		}
		token, err := accounts.BeginEmail(ctx, input.Email, purpose)
		if err != nil {
			s.logger.Error("email link creation failed", "error", err)
			fail(w, 503, "email sign-in unavailable; try again later")
			return
		}
		if token != "" {
			// Fragment tokens are never sent in HTTP URLs, proxy logs or Referer headers.
			link := s.google.Origin + "/#email_token=" + token
			body := "Follow this link to verify your email and choose your Orchestrator password:\n\n" + link + "\n\nThis link expires in 30 minutes and can be used once. If you did not request it, ignore this email. Your account will not change."
			if err = s.mailer.Send(ctx, email.Message{Recipients: []string{input.Email}, Subject: "Your Orchestrator account", Body: body}); err != nil {
				// Do not expose whether the account exists via different public responses.
				s.logger.Warn("account email delivery failed")
			}
		}
		jsonResponse(w, 200, map[string]string{"message": "If this address is eligible, an email will arrive shortly. Check your spam folder. Already registered? Use Sign in or Forgot password."})
	case "/auth/email/login", "/auth/email/complete":
		var u User
		var token string
		var err error
		if r.URL.Path == "/auth/email/login" {
			u, token, err = accounts.PasswordLogin(ctx, input.Email, input.Password)
		} else {
			u, token, err = accounts.CompleteEmail(ctx, input.Token, input.Password, input.Name)
		}
		if err != nil {
			if errors.Is(err, errCredentials) {
				fail(w, 401, err.Error())
			} else if r.URL.Path == "/auth/email/complete" {
				fail(w, 400, "Could not finish. Use a fresh email link and a password of at least 15 characters (maximum 1024 bytes). The site may also be at signup capacity.")
			} else {
				fail(w, 503, "sign-in unavailable; try again later")
			}
			return
		}
		if old, err := r.Cookie("orch_session"); err == nil {
			_ = s.accounts.Logout(ctx, old.Value)
		}
		setCookie(w, "orch_session", token, 7*24*3600, s.google.Origin)
		jsonResponse(w, 200, u)
	default:
		http.NotFound(w, r)
	}
}

// AuthMail uses authenticated SMTP with mandatory STARTTLS and bounded I/O.
// It is independent of job notification SMTP and never simulates account emails.
type AuthMail struct{ Host, Port, Username, Password, From string }

func (m AuthMail) Send(ctx context.Context, msg email.Message) error {
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(m.Host, m.Port))
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	client, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		return err
	}
	defer client.Close()
	if err = client.StartTLS(&tls.Config{ServerName: m.Host, MinVersion: tls.VersionTLS12}); err != nil {
		return err
	}
	if err = client.Auth(smtp.PlainAuth("", m.Username, m.Password, m.Host)); err != nil {
		return err
	}
	if err = client.Mail(m.From); err != nil {
		return err
	}
	for _, recipient := range msg.Recipients {
		if err = client.Rcpt(recipient); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	clean := func(s string) string { return strings.NewReplacer("\r", "", "\n", "").Replace(s) }
	_, err = fmt.Fprintf(writer, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", clean(m.From), clean(strings.Join(msg.Recipients, ", ")), clean(msg.Subject), msg.Body)
	if err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

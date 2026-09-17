package hosted

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/mail"
	"strings"
	"unicode/utf8"
)

var errCredentials = errors.New("email or password is incorrect")
var errEmailLink = errors.New("this email link is invalid or expired; request a new one")

func normalizeEmail(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || len(value) > 254 || !strings.Contains(value, ".") {
		return "", errors.New("enter a valid email address")
	}
	return value, nil
}

func hashPassword(password string) (string, error) {
	if utf8.RuneCountInString(password) < 15 || len(password) > 1024 {
		return "", errors.New("use a password of at least 15 characters and no more than 1024 bytes")
	}
	salt := randomToken()
	key, err := pbkdf2.Key(sha256.New, password, []byte(salt), 600000, 32)
	if err != nil {
		return "", err
	}
	return "pbkdf2-sha256:600000:" + salt + ":" + hex.EncodeToString(key), nil
}
func checkPassword(encoded, password string) bool {
	parts := strings.Split(encoded, ":")
	valid := len(parts) == 4 && parts[0] == "pbkdf2-sha256" && parts[1] == "600000" && len(parts[2]) == 64
	// Do the same expensive work for missing accounts and incorrect passwords.
	salt, expected := strings.Repeat("0", 64), make([]byte, 32)
	if valid {
		salt = parts[2]
		var err error
		expected, err = hex.DecodeString(parts[3])
		valid = err == nil && len(expected) == 32
	}
	key, err := pbkdf2.Key(sha256.New, password, []byte(salt), 600000, 32)
	return err == nil && subtle.ConstantTimeCompare(key, expected) == 1 && valid
}

// BeginEmail creates no account or password until the mailbox owner follows the
// link. Tokens are hashed, short-lived, and single-use; raw values never enter logs.
func (a *Accounts) BeginEmail(ctx context.Context, email, purpose string) (string, error) {
	if purpose != "signup" && purpose != "reset" {
		return "", errEmailLink
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM hosted.email_tokens WHERE expires_at < now()`); err != nil {
		return "", err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM hosted.users WHERE lower(email)=$1`, email).Scan(&count); err != nil {
		return "", err
	}
	if (purpose == "signup" && count != 0) || (purpose == "reset" && count != 1) {
		return "", nil
	}
	var outstanding int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM hosted.email_tokens WHERE email=$1 AND purpose=$2`, email, purpose).Scan(&outstanding); err != nil {
		return "", err
	}
	if outstanding >= 3 {
		return "", nil
	}
	token := randomToken()
	if _, err = tx.ExecContext(ctx, `INSERT INTO hosted.email_tokens(token_hash,email,purpose,expires_at) VALUES($1,$2,$3,now()+interval '30 minutes')`, tokenHash(token), email, purpose); err != nil {
		return "", err
	}
	return token, tx.Commit()
}

func (a *Accounts) CompleteEmail(ctx context.Context, token, password, name string) (User, string, error) {
	if len(token) != 64 {
		return User{}, "", errEmailLink
	}
	encoded, err := hashPassword(password)
	if err != nil {
		return User{}, "", err
	}
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > 100 {
		return User{}, "", errors.New("name must be at most 100 characters")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, "", err
	}
	defer tx.Rollback()
	// Same lock order as Google signup, serializing account capacity and identity linking.
	if _, err = tx.ExecContext(ctx, `LOCK TABLE hosted.users IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return User{}, "", err
	}
	var email, purpose string
	err = tx.QueryRowContext(ctx, `DELETE FROM hosted.email_tokens WHERE token_hash=$1 AND expires_at>now() RETURNING email,purpose`, tokenHash(token)).Scan(&email, &purpose)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", errEmailLink
	}
	if err != nil {
		return User{}, "", err
	}
	var u User
	if purpose == "signup" {
		var count, existing int
		if err = tx.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER (WHERE lower(email)=$1) FROM hosted.users`, email).Scan(&count, &existing); err != nil {
			return u, "", err
		}
		if existing != 0 {
			return u, "", errEmailLink
		}
		if count >= a.maxUsers {
			return u, "", errors.New("website signup capacity reached")
		}
		u = User{ID: randomToken()[:32], Email: email, Name: name}
		if _, err = tx.ExecContext(ctx, `INSERT INTO hosted.users(id,subject,email,name,password_hash) VALUES($1,$2,$3,$4,$5)`, u.ID, "email:"+u.ID, email, name, encoded); err != nil {
			return u, "", err
		}
	} else if purpose == "reset" {
		// Do not choose between ambiguous legacy identities sharing one address.
		err = tx.QueryRowContext(ctx, `SELECT id,email,name FROM hosted.users WHERE lower(email)=$1 AND (SELECT count(*) FROM hosted.users WHERE lower(email)=$1)=1`, email).Scan(&u.ID, &u.Email, &u.Name)
		if errors.Is(err, sql.ErrNoRows) {
			return u, "", errEmailLink
		}
		if err != nil {
			return u, "", err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE hosted.users SET password_hash=$1 WHERE id=$2`, encoded, u.ID); err != nil {
			return u, "", err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM hosted.sessions WHERE user_id=$1`, u.ID); err != nil {
			return u, "", err
		}
	} else {
		return u, "", errEmailLink
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM hosted.email_tokens WHERE email=$1`, email); err != nil {
		return u, "", err
	}
	session := randomToken()
	if _, err = tx.ExecContext(ctx, `INSERT INTO hosted.sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '7 days')`, tokenHash(session), u.ID); err != nil {
		return u, "", err
	}
	return u, session, tx.Commit()
}

func (a *Accounts) PasswordLogin(ctx context.Context, email, password string) (User, string, error) {
	if len(password) > 1024 {
		return User{}, "", errCredentials
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, "", err
	}
	defer tx.Rollback()
	var u User
	var encoded string
	// The row lock makes password verification/session creation atomic with reset.
	err = tx.QueryRowContext(ctx, `SELECT id,email,name,password_hash FROM hosted.users WHERE lower(email)=$1 AND password_hash IS NOT NULL AND (SELECT count(*) FROM hosted.users WHERE lower(email)=$1)=1 FOR UPDATE`, email).Scan(&u.ID, &u.Email, &u.Name, &encoded)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return u, "", err
	}
	if !checkPassword(encoded, password) {
		return User{}, "", errCredentials
	}
	token := randomToken()
	if _, err = tx.ExecContext(ctx, `DELETE FROM hosted.sessions WHERE expires_at < now()`); err != nil {
		return u, "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hosted.sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '7 days')`, tokenHash(token), u.ID); err != nil {
		return u, "", err
	}
	return u, token, tx.Commit()
}

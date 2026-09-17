package hosted

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type Accounts struct {
	db       *sql.DB
	maxUsers int
}

func NewAccounts(ctx context.Context, dsn string, maxUsers int) (*Accounts, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	a := &Accounts{db: db, maxUsers: maxUsers}
	_, err = db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS hosted;
 CREATE TABLE IF NOT EXISTS hosted.users(id text PRIMARY KEY, subject text UNIQUE NOT NULL, email text NOT NULL, name text NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
 CREATE TABLE IF NOT EXISTS hosted.sessions(token_hash text PRIMARY KEY, user_id text NOT NULL REFERENCES hosted.users(id), expires_at timestamptz NOT NULL);
 CREATE INDEX IF NOT EXISTS sessions_expiry ON hosted.sessions(expires_at);`)
	if err == nil {
		_, err = db.ExecContext(ctx, `ALTER TABLE hosted.users ADD COLUMN IF NOT EXISTS password_hash text;
 CREATE TABLE IF NOT EXISTS hosted.email_tokens(token_hash text PRIMARY KEY, email text NOT NULL, purpose text NOT NULL, expires_at timestamptz NOT NULL);
 CREATE INDEX IF NOT EXISTS email_tokens_expiry ON hosted.email_tokens(expires_at);`)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return a, nil
}
func (a *Accounts) Close() error { return a.db.Close() }
func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func tokenHash(s string) string { b := sha256.Sum256([]byte(s)); return hex.EncodeToString(b[:]) }
func (a *Accounts) Login(ctx context.Context, subject, email, name string) (User, string, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, "", err
	}
	defer tx.Rollback()
	// Serialize signup capacity checks across callback requests and server instances.
	if _, err = tx.ExecContext(ctx, `LOCK TABLE hosted.users IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return User{}, "", err
	}
	var u User
	err = tx.QueryRowContext(ctx, `SELECT id,email,name FROM hosted.users WHERE subject=$1`, subject).Scan(&u.ID, &u.Email, &u.Name)
	if errors.Is(err, sql.ErrNoRows) {
		// Only link a verified Google identity to an email-verified password account.
		// Never merge two Google subjects just because their emails match.
		err = tx.QueryRowContext(ctx, `UPDATE hosted.users SET subject=$1 WHERE lower(email)=lower($2) AND subject LIKE 'email:%' RETURNING id,email,name`, subject, email).Scan(&u.ID, &u.Email, &u.Name)
	}
	if errors.Is(err, sql.ErrNoRows) {
		var n int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM hosted.users`).Scan(&n); err != nil {
			return u, "", err
		}
		if n >= a.maxUsers {
			return u, "", errors.New("website signup capacity reached")
		}
		u = User{ID: randomToken()[:32], Email: email, Name: name}
		_, err = tx.ExecContext(ctx, `INSERT INTO hosted.users(id,subject,email,name) VALUES($1,$2,$3,$4)`, u.ID, subject, email, name)
	}
	if err != nil {
		return u, "", err
	}
	token := randomToken()
	if _, err = tx.ExecContext(ctx, `DELETE FROM hosted.sessions WHERE expires_at < now()`); err != nil {
		return u, "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hosted.sessions(token_hash,user_id,expires_at) VALUES($1,$2,$3)`, tokenHash(token), u.ID, time.Now().Add(7*24*time.Hour)); err != nil {
		return u, "", err
	}
	return u, token, tx.Commit()
}
func (a *Accounts) Session(ctx context.Context, token string) (User, error) {
	var u User
	err := a.db.QueryRowContext(ctx, `SELECT u.id,u.email,u.name FROM hosted.users u JOIN hosted.sessions s ON s.user_id=u.id WHERE s.token_hash=$1 AND s.expires_at>now()`, tokenHash(token)).Scan(&u.ID, &u.Email, &u.Name)
	return u, err
}
func (a *Accounts) Logout(ctx context.Context, token string) error {
	_, err := a.db.ExecContext(ctx, `DELETE FROM hosted.sessions WHERE token_hash=$1`, tokenHash(token))
	return err
}
func (a *Accounts) Users(ctx context.Context) ([]User, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT id,email,name FROM hosted.users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.Name); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// AcquireHost ensures this initial deployment runs one scheduler per workspace
// and one writer for each user's persistent Codex credential directory.
func (a *Accounts) AcquireHost(ctx context.Context) (func(), error) {
	conn, err := a.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	var acquired bool
	if err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(739281642019)`).Scan(&acquired); err != nil || !acquired {
		conn.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("another hosted instance already owns this database")
	}
	return func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(739281642019)`)
		_ = conn.Close()
	}, nil
}

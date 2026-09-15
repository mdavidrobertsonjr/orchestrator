package hosted

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Identity struct {
	Subject  string `json:"sub"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Verified bool   `json:"email_verified"`
}
type GoogleAuth struct {
	ClientID, ClientSecret, Origin string
	Client                         *http.Client
}

func (g GoogleAuth) Start(w http.ResponseWriter, r *http.Request) {
	state, verifier := randomToken(), randomToken()
	setCookie(w, "orch_oauth", state+"."+verifier, 600, g.Origin)
	challenge := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {g.ClientID}, "redirect_uri": {g.Origin + "/auth/google/callback"}, "response_type": {"code"}, "scope": {"openid email profile"}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}}
	http.Redirect(w, r, "https://accounts.google.com/o/oauth2/v2/auth?"+q.Encode(), http.StatusFound)
}
func (g GoogleAuth) Callback(w http.ResponseWriter, r *http.Request) (Identity, error) {
	cookie, err := r.Cookie("orch_oauth")
	setCookie(w, "orch_oauth", "", -1, g.Origin)
	if err != nil {
		return Identity{}, errors.New("sign-in expired; please try again")
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 || r.URL.Query().Get("state") == "" || subtle.ConstantTimeCompare([]byte(parts[0]), []byte(r.URL.Query().Get("state"))) != 1 {
		return Identity{}, errors.New("invalid sign-in state")
	}
	if r.URL.Query().Get("code") == "" {
		return Identity{}, errors.New("Google sign-in was not completed")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	form := url.Values{"client_id": {g.ClientID}, "client_secret": {g.ClientSecret}, "code": {r.URL.Query().Get("code")}, "code_verifier": {parts[1]}, "grant_type": {"authorization_code"}, "redirect_uri": {g.Origin + "/auth/google/callback"}}
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return Identity{}, errors.New("Google sign-in unavailable")
	}
	defer resp.Body.Close()
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&token) != nil || token.AccessToken == "" {
		return Identity{}, errors.New("Google sign-in failed")
	}
	// Fetch identity directly from Google's authenticated endpoint; never trust browser-supplied profile fields or an unverified JWT.
	req, _ = http.NewRequestWithContext(ctx, "GET", "https://openidconnect.googleapis.com/v1/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	resp, err = client.Do(req)
	if err != nil {
		return Identity{}, errors.New("Google identity unavailable")
	}
	defer resp.Body.Close()
	var identity Identity
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&identity) != nil || identity.Subject == "" || !identity.Verified || identity.Email == "" {
		return Identity{}, errors.New("a verified Google email is required")
	}
	return identity, nil
}
func setCookie(w http.ResponseWriter, name, value string, maxAge int, origin string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(origin, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}
func sameOrigin(r *http.Request, origin string) bool {
	return r.Header.Get("Origin") == origin && r.Header.Get("Sec-Fetch-Site") != "cross-site"
}
func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]string{"error": message})
}

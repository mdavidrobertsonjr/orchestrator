package hosted

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestGoogleStateAndPKCE(t *testing.T) {
	g := GoogleAuth{ClientID: "client", ClientSecret: "secret", Origin: "https://jobs.example"}
	w := httptest.NewRecorder()
	g.Start(w, httptest.NewRequest("GET", "/auth/google", nil))
	location, _ := url.Parse(w.Header().Get("Location"))
	q := location.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("redirect_uri") != "https://jobs.example/auth/google/callback" {
		t.Fatal(q)
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("unsafe cookie")
	}
	calls := 0
	g.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body := `{"access_token":"server-token"}`
		if calls == 1 {
			r.ParseForm()
			if r.Form.Get("code_verifier") != strings.Split(cookie.Value, ".")[1] {
				t.Fatal("missing PKCE verifier")
			}
		} else {
			if r.Header.Get("Authorization") != "Bearer server-token" {
				t.Fatal("missing authorization")
			}
			body = `{"sub":"google-subject","email":"test@example.com","email_verified":true,"name":"Test"}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}
	req := httptest.NewRequest("GET", "/auth/google/callback?code=code&state=wrong", nil)
	req.AddCookie(cookie)
	if _, err := g.Callback(httptest.NewRecorder(), req); err == nil || calls != 0 {
		t.Fatal("invalid state accepted")
	}
	req = httptest.NewRequest("GET", "/auth/google/callback?code=code&state="+q.Get("state"), nil)
	req.AddCookie(cookie)
	identity, err := g.Callback(httptest.NewRecorder(), req)
	if err != nil || identity.Subject != "google-subject" || calls != 2 {
		t.Fatal(identity, err, calls)
	}
	encoded, _ := json.Marshal(identity)
	if strings.Contains(string(encoded), "server-token") {
		t.Fatal("token exposed")
	}
}
func TestSameOrigin(t *testing.T) {
	for _, origin := range []string{"", "https://evil.example", "https://jobs.example"} {
		r := httptest.NewRequest("POST", "/auth/logout", nil)
		r.Header.Set("Origin", origin)
		if sameOrigin(r, "https://jobs.example") != (origin == "https://jobs.example") {
			t.Fatal(origin)
		}
	}
}

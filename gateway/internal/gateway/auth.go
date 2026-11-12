package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Provider is one OAuth2 sign-in provider. URLs are fields so tests can point
// them at a fake server.
type Provider struct {
	Name         string
	AuthURL      string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       []string
	// Identify returns the provider's stable subject id and a display name,
	// or ErrUnverified if the account has no verified email.
	Identify func(ctx context.Context, c *http.Client, token string) (subject, name string, err error)
}

var ErrUnverified = errors.New("the provider reports no verified email")

// GitHub signs in with a GitHub OAuth app. The primary email must be
// verified (GET /user/emails).
func GitHub(clientID, secret, apiBase string) Provider {
	return Provider{
		Name: "github", AuthURL: "https://github.com/login/oauth/authorize",
		TokenURL: "https://github.com/login/oauth/access_token",
		ClientID: clientID, ClientSecret: secret, Scopes: []string{"read:user", "user:email"},
		Identify: func(ctx context.Context, c *http.Client, token string) (string, string, error) {
			var user struct {
				ID    int64  `json:"id"`
				Login string `json:"login"`
			}
			if err := getJSON(ctx, c, apiBase+"/user", token, &user); err != nil {
				return "", "", err
			}
			var emails []struct {
				Primary  bool `json:"primary"`
				Verified bool `json:"verified"`
			}
			if err := getJSON(ctx, c, apiBase+"/user/emails", token, &emails); err != nil {
				return "", "", err
			}
			for _, e := range emails {
				if e.Primary && e.Verified && user.ID != 0 {
					return strconv.FormatInt(user.ID, 10), user.Login, nil
				}
			}
			return "", "", ErrUnverified
		},
	}
}

// Google signs in with Google. The userinfo endpoint is called over TLS with
// the access token, so its answer needs no ID-token signature check.
func Google(clientID, secret, userinfoURL string) Provider {
	return Provider{
		Name: "google", AuthURL: "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL: "https://oauth2.googleapis.com/token",
		ClientID: clientID, ClientSecret: secret, Scopes: []string{"openid", "email", "profile"},
		Identify: func(ctx context.Context, c *http.Client, token string) (string, string, error) {
			var info struct {
				Sub           string `json:"sub"`
				Name          string `json:"name"`
				EmailVerified bool   `json:"email_verified"`
			}
			if err := getJSON(ctx, c, userinfoURL, token, &info); err != nil {
				return "", "", err
			}
			if !info.EmailVerified || info.Sub == "" {
				return "", "", ErrUnverified
			}
			return info.Sub, info.Name, nil
		},
	}
}

func getJSON(ctx context.Context, c *http.Client, u, token string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}

const (
	sessionCookie = "glasshouse_session"
	oauthCookie   = "glasshouse_oauth"
	sessionTTL    = 12 * time.Hour
	oauthTTL      = 10 * time.Minute
)

// oauthState lives in a short-lived signed cookie between login and callback.
type oauthState struct {
	State    string `json:"st"`
	Verifier string `json:"v"`
	Provider string `json:"p"`
	Next     string `json:"n"`
	Expires  int64  `json:"e"`
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// safeNext keeps the post-login redirect on this site: a path, never a
// scheme-relative "//host" or backslash trick.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n") {
		return "/"
	}
	return next
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	p, ok := s.providers[r.PathValue("provider")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	st := oauthState{
		State: randomToken(), Verifier: randomToken(), Provider: p.Name,
		Next: safeNext(r.URL.Query().Get("next")), Expires: s.now().Add(oauthTTL).Unix(),
	}
	token, err := s.signer.Sign(oauthCookie, st)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.setCookie(w, oauthCookie, token, oauthTTL)
	challenge := sha256.Sum256([]byte(st.Verifier))
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {p.ClientID},
		"redirect_uri":          {s.callbackURL(p.Name)},
		"scope":                 {strings.Join(p.Scopes, " ")},
		"state":                 {st.State},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}
	http.Redirect(w, r, p.AuthURL+"?"+q.Encode(), http.StatusFound)
}

func (s *Server) callbackURL(provider string) string {
	return s.publicURL + "/auth/" + provider + "/callback"
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	p, ok := s.providers[r.PathValue("provider")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	var st oauthState
	c, err := r.Cookie(oauthCookie)
	if err != nil || s.signer.Verify(oauthCookie, c.Value, &st) != nil ||
		s.now().Unix() >= st.Expires || st.Provider != p.Name ||
		subtle.ConstantTimeCompare([]byte(st.State), []byte(r.URL.Query().Get("state"))) != 1 {
		http.Error(w, "sign-in expired or did not match; please try again", http.StatusBadRequest)
		return
	}
	s.setCookie(w, oauthCookie, "", -1)
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Redirect(w, r, st.Next, http.StatusFound) // the reader cancelled at the provider
		return
	}
	token, err := s.exchange(r.Context(), p, code, st.Verifier)
	if err != nil {
		s.log.Error("oauth exchange failed", "provider", p.Name, "err", err)
		http.Error(w, "sign-in failed", http.StatusBadGateway)
		return
	}
	subject, name, err := p.Identify(r.Context(), s.client, token)
	if errors.Is(err, ErrUnverified) {
		http.Error(w, "sign-in refused: your "+p.Name+" account needs a verified email address", http.StatusForbidden)
		return
	}
	if err != nil {
		s.log.Error("oauth identify failed", "provider", p.Name, "err", err)
		http.Error(w, "sign-in failed", http.StatusBadGateway)
		return
	}
	sess := Session{Provider: p.Name, Subject: subject, Name: name, Expires: s.now().Add(sessionTTL).Unix()}
	value, err := s.signer.Sign(sessionCookie, sess)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.setCookie(w, sessionCookie, value, sessionTTL)
	http.Redirect(w, r, st.Next+"#try-it-yourself", http.StatusFound)
}

func (s *Server) exchange(ctx context.Context, p Provider, code, verifier string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {s.callbackURL(p.Name)},
		"client_id":     {p.ClientID},
		"client_secret": {p.ClientSecret},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK || body.AccessToken == "" {
		return "", fmt.Errorf("token endpoint: HTTP %d %s", resp.StatusCode, body.Error)
	}
	return body.AccessToken, nil
}

// session returns the signed-in reader, if any.
func (s *Server) session(r *http.Request) (Session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return Session{}, false
	}
	var sess Session
	if s.signer.Verify(sessionCookie, c.Value, &sess) != nil || sess.expired(s.now()) || sess.Subject == "" {
		return Session{}, false
	}
	return sess, true
}

// setCookie sets a session-scoped cookie; a negative ttl deletes it.
func (s *Server) setCookie(w http.ResponseWriter, name, value string, ttl time.Duration) {
	maxAge := int(ttl.Seconds())
	if ttl < 0 {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
}

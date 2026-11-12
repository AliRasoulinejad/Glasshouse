package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Signer signs and verifies small JSON values carried in cookies. There is
// no user database: the signed cookie is the whole session.
type Signer struct{ key []byte }

func NewSigner(key []byte) (*Signer, error) {
	if len(key) < 32 {
		return nil, errors.New("session key must be at least 32 bytes")
	}
	return &Signer{key: key}, nil
}

// Sign returns base64url(json(v)) + "." + base64url(hmac). v must carry its
// own expiry; see Verify.
func (s *Signer) Sign(purpose string, v any) (string, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	return payload + "." + s.mac(purpose, payload), nil
}

// Verify checks the signature for purpose and decodes into v. purpose keeps
// one cookie kind from being replayed as another (an OAuth state cookie as a
// session, say).
func (s *Signer) Verify(purpose, token string, v any) error {
	payload, sig, ok := strings.Cut(token, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(s.mac(purpose, payload))) {
		return errors.New("bad signature")
	}
	body, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

func (s *Signer) mac(purpose, payload string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(purpose + "\x00" + payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Session is the signed-in reader: provider plus the provider's stable
// subject id. Name is only for display. Nothing else is kept.
type Session struct {
	Provider string `json:"p"`
	Subject  string `json:"s"`
	Name     string `json:"n"`
	Expires  int64  `json:"e"`
}

// Key identifies one reader for the one-lab rule. The same person signed in
// through both providers counts as two readers.
func (s Session) Key() string { return s.Provider + ":" + s.Subject }

func (s Session) expired(now time.Time) bool { return now.Unix() >= s.Expires }

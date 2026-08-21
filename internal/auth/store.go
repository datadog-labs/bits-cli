package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

const (
	keyringService = "com.datadog.bits-cli"
	keyringAccount = "oauth-session"
)

// ErrNoSession means no OAuth login is present in the OS credential store.
var ErrNoSession = errors.New("no Bits CLI OAuth session")

// Session is the durable subset of an OAuth token plus the routing information
// needed to refresh it and call the Assistant API.
type Session struct {
	Site         string    `json:"site"`
	ClientID     string    `json:"client_id"`
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
}

func sessionFromToken(cfg SiteConfig, token *oauth2.Token) Session {
	return Session{
		Site:         cfg.Site,
		ClientID:     cfg.ClientID,
		AccessToken:  token.AccessToken,
		TokenType:    token.TokenType,
		RefreshToken: token.RefreshToken,
		Expiry:       token.Expiry,
	}
}

func (s Session) token() *oauth2.Token {
	return &oauth2.Token{
		AccessToken:  s.AccessToken,
		TokenType:    s.TokenType,
		RefreshToken: s.RefreshToken,
		Expiry:       s.Expiry,
	}
}

func (s Session) validate() error {
	if s.Site == "" || s.ClientID == "" || s.AccessToken == "" {
		return fmt.Errorf("stored OAuth session is incomplete")
	}
	return nil
}

// CredentialStore persists OAuth sessions. Implementations must protect both
// access and rotating refresh tokens as secrets.
type CredentialStore interface {
	Load() (Session, error)
	Save(Session) error
	Delete() error
}

// KeyringStore stores the single active Bits CLI session in the native OS
// credential manager (Keychain, Secret Service, or Windows Credential Manager).
type KeyringStore struct{}

func (KeyringStore) Load() (Session, error) {
	raw, err := keyring.Get(keyringService, keyringAccount)
	if errors.Is(err, keyring.ErrNotFound) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, fmt.Errorf("read OAuth session from OS credential store: %w", err)
	}
	var session Session
	if err := json.Unmarshal([]byte(raw), &session); err != nil {
		return Session{}, fmt.Errorf("decode OAuth session from OS credential store: %w", err)
	}
	if err := session.validate(); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (KeyringStore) Save(session Session) error {
	if err := session.validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("encode OAuth session: %w", err)
	}
	if err := keyring.Set(keyringService, keyringAccount, string(raw)); err != nil {
		return fmt.Errorf("write OAuth session to OS credential store: %w", err)
	}
	return nil
}

func (KeyringStore) Delete() error {
	err := keyring.Delete(keyringService, keyringAccount)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete OAuth session from OS credential store: %w", err)
	}
	return nil
}

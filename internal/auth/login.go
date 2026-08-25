package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

const (
	loginTimeout          = 5 * time.Minute
	sessionLockTimeout    = 45 * time.Second
	sessionPersistTimeout = 5 * time.Second
)

// LoginOptions contains the testable dependencies for an interactive login.
type LoginOptions struct {
	Site       string
	ClientID   string
	Store      CredentialStore
	HTTPClient *http.Client
	OpenURL    func(string) error
	Out        io.Writer
}

// printf writes a best-effort status message to the configured output. Output
// is advisory, so write failures are intentionally ignored.
func (o LoginOptions) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(o.Out, format, args...)
}

// Login runs Authorization Code + PKCE through the registered loopback
// callback and stores the resulting tokens in the OS credential manager.
func Login(ctx context.Context, opts LoginOptions) (Session, error) {
	cfg, err := ConfigForSite(opts.Site, opts.ClientID)
	if err != nil {
		return Session{}, err
	}
	return login(ctx, cfg, opts)
}

func login(ctx context.Context, cfg SiteConfig, opts LoginOptions) (Session, error) {
	if opts.Store == nil {
		opts.Store = KeyringStore{}
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.OpenURL == nil {
		opts.OpenURL = openBrowser
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}

	verifier := oauth2.GenerateVerifier()
	state := oauth2.GenerateVerifier()
	listener, callback, err := listenForCallback(cfg.RedirectURI, state)
	if err != nil {
		return Session{}, err
	}
	cfg.RedirectURI = listener.redirectURI
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = listener.server.Shutdown(shutdownCtx)
	}()

	authURL := cfg.OAuth2Config().AuthCodeURL(
		state,
		oauth2.S256ChallengeOption(verifier),
	)
	opts.printf("Opening Datadog login in your browser…\nIf it does not open, visit:\n%s\n", authURL)
	if err := opts.OpenURL(authURL); err != nil {
		opts.printf("Could not open a browser automatically: %v\n", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()
	var code, callbackDomain string
	select {
	case <-waitCtx.Done():
		return Session{}, fmt.Errorf("wait for OAuth callback: %w", waitCtx.Err())
	case result := <-callback:
		if result.err != nil {
			return Session{}, result.err
		}
		code = result.code
		callbackDomain = result.domain
	}
	cfg, err = cfg.WithCallbackDomain(callbackDomain)
	if err != nil {
		return Session{}, err
	}
	opts.printf("Datadog OAuth callback domain: %s\n", cfg.Domain)

	exchangeCtx, exchangeCancel := context.WithTimeout(ctx, 30*time.Second)
	defer exchangeCancel()
	exchangeCtx = context.WithValue(exchangeCtx, oauth2.HTTPClient, opts.HTTPClient)
	token, err := cfg.OAuth2Config().Exchange(exchangeCtx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Session{}, sanitizedOAuthError("exchange Datadog OAuth code", err)
	}
	session := sessionFromToken(cfg, token)
	var previous Session
	var hadPrevious, saveCommitted bool
	lockCtx, lockCancel := context.WithTimeout(ctx, sessionLockTimeout)
	defer lockCancel()
	persistErr := withSessionLock(lockCtx, opts.Store, func() error {
		stored, loadErr := opts.Store.Load()
		if loadErr == nil {
			previous, hadPrevious = stored, true
		} else if errors.Is(loadErr, ErrNoSession) || errors.Is(loadErr, ErrSessionCorrupt) {
			// Continue and replace the missing or corrupt session.
		} else {
			return loadErr
		}
		persistCtx, persistCancel := context.WithTimeout(ctx, sessionPersistTimeout)
		defer persistCancel()
		if err := saveWithRetry(persistCtx, opts.Store, session); err != nil {
			storedAfter, verifyErr := opts.Store.Load()
			if verifyErr == nil && sameSession(storedAfter, session) {
				saveCommitted = true
				return nil
			}
			if verifyErr != nil && !errors.Is(verifyErr, ErrNoSession) {
				return fmt.Errorf("%w: %w", ErrSessionMutationUnknown, errors.Join(err, verifyErr))
			}
			return err
		}
		saveCommitted = true
		return nil
	})
	if persistErr != nil && saveCommitted && errors.Is(persistErr, ErrSessionUnlock) {
		opts.printf("Login saved, but releasing the session lock failed; restart Bits before continuing: %v\n", persistErr)
		persistErr = nil
	}
	if persistErr != nil && errors.Is(persistErr, ErrSessionMutationUnknown) {
		return Session{}, fmt.Errorf("persist new OAuth session: %w; not revoking because the durable outcome could not be verified", persistErr)
	}
	if persistErr != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		revokeErr := Revoke(cleanupCtx, session, opts.HTTPClient)
		cleanupCancel()
		if revokeErr != nil {
			cleanupErr := fmt.Errorf("cleanup revocation failed: %w", revokeErr)
			return Session{}, fmt.Errorf("persist new OAuth session: %w", errors.Join(persistErr, cleanupErr))
		}
		return Session{}, fmt.Errorf("persist new OAuth session: %w; the unpersisted grant was revoked", persistErr)
	}

	// Replacement login is commit-then-cleanup: the new grant is durable before
	// the old one is revoked, so a revocation outage cannot destroy the login.
	if hadPrevious && sessionRevocationCredential(previous) != sessionRevocationCredential(session) {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		revokeErr := Revoke(cleanupCtx, previous, opts.HTTPClient)
		cleanupCancel()
		if revokeErr != nil {
			opts.printf("New login saved; previous token revocation failed: %v\n", revokeErr)
		}
	}
	return session, nil
}

type callbackResult struct {
	code   string
	domain string
	err    error
}

type callbackListener struct {
	server      *http.Server
	redirectURI string
}

func listenForCallback(redirectURI, wantState string) (*callbackListener, <-chan callbackResult, error) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return nil, nil, fmt.Errorf("parse OAuth redirect URI: %w", err)
	}
	if u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path == "" || !strings.HasPrefix(u.Path, "/") {
		return nil, nil, fmt.Errorf("OAuth redirect must use an IPv4 loopback literal, explicit port, and absolute path")
	}
	ln, err := net.Listen("tcp4", net.JoinHostPort(u.Hostname(), u.Port()))
	if err != nil {
		return nil, nil, fmt.Errorf("listen for OAuth callback on %s: %w", u.Host, err)
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !tcpAddr.IP.IsLoopback() || tcpAddr.Port < 1 {
		_ = ln.Close()
		return nil, nil, fmt.Errorf("OAuth callback listener did not bind an IPv4 loopback port")
	}
	u.Host = net.JoinHostPort("127.0.0.1", strconv.Itoa(tcpAddr.Port))
	actualRedirectURI := u.String()

	results := make(chan callbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(u.Path, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("state") != wantState {
			// Ignore unsolicited localhost probes rather than letting them cancel
			// the real browser flow. A matching state remains mandatory.
			http.Error(w, "OAuth state did not match. Return to the terminal and try again.", http.StatusBadRequest)
			return
		}
		if oauthErr := query.Get("error"); oauthErr != "" {
			code := safeOAuthErrorCode(oauthErr)
			if code == "" {
				code = "authorization_error"
			}
			http.Error(w, "Datadog login was not completed. Return to the terminal.", http.StatusBadRequest)
			select {
			case results <- callbackResult{err: fmt.Errorf("datadog OAuth authorization failed: %s", code)}:
			default:
			}
			return
		}
		code := query.Get("code")
		if code == "" {
			http.Error(w, "Missing OAuth authorization code.", http.StatusBadRequest)
			select {
			case results <- callbackResult{err: errors.New("OAuth callback did not include an authorization code")}:
			default:
			}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, "<!doctype html><title>Bits CLI login complete</title><h1>Login complete</h1><p>You can close this tab and return to Bits CLI.</p><small>%s</small>", html.EscapeString(u.Host))
		select {
		case results <- callbackResult{code: code, domain: strings.TrimSpace(query.Get("domain"))}:
		default:
		}
	})
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       10 * time.Second,
	}
	go func() {
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case results <- callbackResult{err: fmt.Errorf("serve OAuth callback: %w", err)}:
			default:
			}
		}
	}()
	return &callbackListener{server: server, redirectURI: actualRedirectURI}, results, nil
}

type revocationCredential struct {
	token string
	hint  string
}

func sessionRevocationCredential(session Session) revocationCredential {
	if session.RefreshToken != "" {
		return revocationCredential{token: session.RefreshToken, hint: "refresh_token"}
	}
	return revocationCredential{token: session.AccessToken, hint: "access_token"}
}

// Revoke invalidates the refresh token when available, otherwise the access
// token. Callers should delete the local session even if this best-effort call
// fails.
func Revoke(ctx context.Context, session Session, httpClient *http.Client) error {
	cfg, err := ConfigForSite(session.Site, session.ClientID)
	if err != nil {
		return err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	target := sessionRevocationCredential(session)
	form := url.Values{
		"client_id":       {cfg.ClientID},
		"token":           {target.token},
		"token_type_hint": {target.hint},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.RevokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("revoke Datadog OAuth token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var oauthErr struct {
			Code string `json:"error"`
		}
		if json.Unmarshal(body, &oauthErr) == nil {
			if code := safeOAuthErrorCode(oauthErr.Code); code != "" {
				return fmt.Errorf("revoke Datadog OAuth token: HTTP %d (%s)", resp.StatusCode, code)
			}
		}
		return fmt.Errorf("revoke Datadog OAuth token: HTTP %d", resp.StatusCode)
	}
	return nil
}

// Logout atomically removes the current local session before best-effort
// remote revocation. A refreshing process that was waiting on the same lock
// will observe deletion and cannot resurrect the session.
func Logout(ctx context.Context, store CredentialStore, httpClient *http.Client) (bool, error, error) {
	if store == nil {
		store = KeyringStore{}
	}
	var session Session
	var hadSession, localDeleted bool
	err := withSessionLock(ctx, store, func() error {
		stored, loadErr := store.Load()
		if errors.Is(loadErr, ErrNoSession) {
			return nil
		}
		if errors.Is(loadErr, ErrSessionCorrupt) {
			hadSession = true
			if err := deleteWithRetry(ctx, store); err != nil {
				return err
			}
			localDeleted = true
			return nil
		}
		if loadErr != nil {
			return loadErr
		}
		session, hadSession = stored, true
		if err := deleteWithRetry(ctx, store); err != nil {
			return err
		}
		localDeleted = true
		return nil
	})
	if err != nil && (!localDeleted || !errors.Is(err, ErrSessionUnlock)) {
		return hadSession, nil, err
	}
	if !hadSession {
		return false, nil, nil
	}
	if session.AccessToken == "" {
		// A corrupt credential was removed but cannot be safely revoked because
		// its token/client/routing fields were not trusted.
		return true, nil, nil
	}
	return true, Revoke(ctx, session, httpClient), nil
}

func sanitizedOAuthError(operation string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	var retrieveErr *oauth2.RetrieveError
	if !errors.As(err, &retrieveErr) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	status := 0
	if retrieveErr.Response != nil {
		status = retrieveErr.Response.StatusCode
	}
	if code := safeOAuthErrorCode(retrieveErr.ErrorCode); code != "" {
		return fmt.Errorf("%s: HTTP %d (%s)", operation, status, code)
	}
	return fmt.Errorf("%s: HTTP %d", operation, status)
}

func safeOAuthErrorCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 64 {
		return ""
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' && r != '.' {
			return ""
		}
	}
	return code
}

func openBrowser(target string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command, args = "open", []string{target}
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		command, args = "xdg-open", []string{target}
	}
	cmd := exec.Command(command, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

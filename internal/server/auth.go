// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"math"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Login with one shared user name and password (KAMERTON_AUTH_USER /
// KAMERTON_AUTH_PASSWORD). Sessions are stateless: the cookie carries the
// user and the expiry, signed with HMAC-SHA256, so they survive restarts and
// work across replicas as long as every replica has the same key.

const (
	// SessionCookie is the name of the session cookie.
	SessionCookie = "kamerton_session"
	// SessionTTL is how long a session lasts.
	SessionTTL = 12 * time.Hour

	// Failed logins allowed per client address within loginWindow.
	loginMaxFailures = 5
	loginWindow      = time.Minute

	// keyIterations is the PBKDF2 cost of deriving the signing key from the
	// password: a stolen cookie should not make guessing the password cheap.
	keyIterations = 200_000

	tokenVersion = "v1"
)

// Auth checks credentials and sessions. A nil *Auth means login is disabled.
type Auth struct {
	user     string
	userHash [32]byte
	passHash [32]byte
	key      []byte

	limiter *loginLimiter
	now     func() time.Time
}

// NewAuth configures login. secret is the session signing key; when empty,
// the key is derived from the user and password, so sessions survive
// restarts, are valid on every replica and end when the password changes.
func NewAuth(user, password, secret string) *Auth {
	a := &Auth{
		user:     user,
		userHash: sha256.Sum256([]byte(user)),
		passHash: sha256.Sum256([]byte(password)),
		limiter:  newLoginLimiter(loginMaxFailures, loginWindow),
		now:      time.Now,
	}
	if secret != "" {
		k := sha256.Sum256([]byte("kamerton-session|" + secret))
		a.key = k[:]
	} else {
		k, err := pbkdf2.Key(sha256.New, password, []byte("kamerton-session|"+user), keyIterations, 32)
		if err != nil {
			// Only fails for invalid parameters (not the case here) or in FIPS-only mode with a short password.
			s := sha256.Sum256([]byte("kamerton-session|" + user + "\x00" + password))
			k = s[:]
		}
		a.key = k
	}
	return a
}

// check compares the credentials in constant time. Both comparisons always run.
func (a *Auth) check(user, password string) bool {
	u := sha256.Sum256([]byte(user))
	p := sha256.Sum256([]byte(password))
	okUser := subtle.ConstantTimeCompare(u[:], a.userHash[:])
	okPass := subtle.ConstantTimeCompare(p[:], a.passHash[:])
	return okUser&okPass == 1
}

func (a *Auth) sign(payload string) string {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// issue returns a session token for the user valid until exp.
func (a *Auth) issue(exp time.Time) string {
	payload := tokenVersion + "|" + strconv.FormatInt(exp.Unix(), 10) + "|" + a.user
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + a.sign(payload)
}

// verify returns the user of a valid, unexpired token.
func (a *Auth) verify(token string) (string, bool) {
	b64, sig, ok := strings.Cut(token, ".")
	if !ok {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil {
		return "", false
	}
	payload := string(raw)
	if !hmac.Equal([]byte(sig), []byte(a.sign(payload))) {
		return "", false
	}
	parts := strings.SplitN(payload, "|", 3)
	if len(parts) != 3 || parts[0] != tokenVersion {
		return "", false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || a.now().Unix() >= exp {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(parts[2]), []byte(a.user)) != 1 {
		return "", false
	}
	return parts[2], true
}

// session returns the user of the request's session cookie.
func (a *Auth) session(r *http.Request) (string, bool) {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return "", false
	}
	return a.verify(c.Value)
}

// isHTTPS reports whether the client reached us over HTTPS, directly or
// through a proxy that sets X-Forwarded-Proto.
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

func (a *Auth) setCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// clientAddr is the address the limiter counts failures for. It is the
// TCP peer, not X-Forwarded-For, which a client could set to anything; behind
// an ingress all clients therefore share one limit.
func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// publicAPI are the API paths open without a session when login is enabled.
var publicAPI = map[string]bool{
	"/api/v1/health":    true, // probes
	"/api/auth/login":   true,
	"/api/auth/session": true,
	"/api/auth/logout":  true,
}

// machineAPI are API paths called by programs, not browsers: they check
// their own credentials (the Alertmanager webhook checks a bearer token)
// and are exempt from the session and the CSRF header.
var machineAPI = map[string]bool{
	AlertmanagerHookPath: true,
}

// csrfHeader must be present on state-changing API requests. Browsers do not
// let another site send a custom header without a CORS preflight, which this
// server never approves; the UI always sets it. It is required with login
// disabled too: otherwise any web page opened by someone who can reach
// Kamerton could change its settings with a plain form POST.
const csrfHeader = "X-Requested-With"

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// protect wraps the API with the CSRF check and, when login is enabled, the
// session check. The UI's static files (and /healthz) stay public: the login
// page has to load.
func (s *Server) protect(next http.Handler) http.Handler {
	a := s.opts.Auth
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The mux routes by the cleaned path, so the checks use it too.
		p := path.Clean("/" + r.URL.Path)
		if !strings.HasPrefix(p, "/api/") || machineAPI[p] {
			next.ServeHTTP(w, r)
			return
		}
		if isMutating(r.Method) && r.Header.Get(csrfHeader) == "" {
			writeError(w, http.StatusForbidden, "csrf", "missing "+csrfHeader+" header")
			return
		}
		if a == nil || publicAPI[p] {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := a.session(r); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) authSession(w http.ResponseWriter, r *http.Request) {
	a := s.opts.Auth
	if a == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "authenticated": false, "user": ""})
		return
	}
	user, ok := a.session(r)
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "authenticated": ok, "user": user})
}

func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	a := s.opts.Auth
	if a == nil {
		writeError(w, http.StatusBadRequest, "auth_disabled", "login is not enabled")
		return
	}
	addr := clientAddr(r)
	if wait := a.limiter.blocked(addr, a.now()); wait > 0 {
		writeTooMany(w, wait)
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var req loginRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "expected JSON {\"username\", \"password\"}")
		return
	}
	if !a.check(req.Username, req.Password) {
		// The attempt that reaches the limit still gets 401; the next ones get 429.
		a.limiter.fail(addr, a.now())
		s.log.Warn("login failed", "remote", addr)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid user name or password")
		return
	}
	a.limiter.reset(addr)
	a.setCookie(w, r, a.issue(a.now().Add(SessionTTL)), int(SessionTTL.Seconds()))
	s.log.Info("login", "user", a.user, "remote", addr)
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "authenticated": true, "user": a.user})
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	if a := s.opts.Auth; a != nil {
		a.setCookie(w, r, "", -1)
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
}

func writeTooMany(w http.ResponseWriter, wait time.Duration) {
	secs := int(math.Ceil(wait.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many failed login attempts", "code": "too_many_attempts", "retryAfter": secs})
}

// loginLimiter counts failed logins per address in a sliding window.
type loginLimiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	failures map[string][]time.Time
}

func newLoginLimiter(maxFailures int, window time.Duration) *loginLimiter {
	return &loginLimiter{max: maxFailures, window: window, failures: map[string][]time.Time{}}
}

// recent drops failures older than the window; the caller holds mu.
func (l *loginLimiter) recent(addr string, now time.Time) []time.Time {
	list := l.failures[addr]
	i := 0
	for i < len(list) && now.Sub(list[i]) >= l.window {
		i++
	}
	list = list[i:]
	if len(list) == 0 {
		delete(l.failures, addr)
		return nil
	}
	l.failures[addr] = list
	return list
}

// waitFor is how long addr stays blocked; the caller holds mu.
func (l *loginLimiter) waitFor(list []time.Time, now time.Time) time.Duration {
	if len(list) < l.max {
		return 0
	}
	return list[len(list)-l.max].Add(l.window).Sub(now)
}

// blocked returns how long addr must wait, or 0.
func (l *loginLimiter) blocked(addr string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.waitFor(l.recent(addr, now), now)
}

// fail records a failure and returns how long addr is now blocked, or 0.
func (l *loginLimiter) fail(addr string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.failures) > 10_000 {
		// Keep memory bounded under a spray of addresses.
		for k := range l.failures {
			l.recent(k, now)
		}
	}
	list := append(l.recent(addr, now), now)
	l.failures[addr] = list
	return l.waitFor(list, now)
}

func (l *loginLimiter) reset(addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, addr)
}

// LogAuthMode reports at startup whether login is enabled (never the password).
func LogAuthMode(log *slog.Logger, a *Auth) {
	if a == nil {
		log.Warn("login is disabled: anyone who can reach the UI can change settings; set KAMERTON_AUTH_USER and KAMERTON_AUTH_PASSWORD to enable it")
		return
	}
	log.Info("login is enabled", "user", a.user)
}

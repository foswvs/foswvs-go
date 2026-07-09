package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const sessionTTL = 10 * time.Hour

// SessionStore manages admin authentication with server-side sessions.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]time.Time // token -> expiry
}

func NewSessionStore() *SessionStore {
	s := &SessionStore{sessions: make(map[string]time.Time)}
	go s.cleanupLoop()
	return s
}

func (s *SessionStore) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		s.mu.Lock()
		now := time.Now()
		for tok, exp := range s.sessions {
			if now.After(exp) {
				delete(s.sessions, tok)
			}
		}
		s.mu.Unlock()
	}
}

// CreateSession generates a new session token.
func (s *SessionStore) CreateSession() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)

	s.mu.Lock()
	s.sessions[token] = time.Now().Add(sessionTTL)
	s.mu.Unlock()

	return token, nil
}

// ValidateSession checks if a session token is valid.
func (s *SessionStore) ValidateSession(token string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	exp, ok := s.sessions[token]
	if !ok {
		return false
	}
	return time.Now().Before(exp)
}

// DestroySession removes a session.
func (s *SessionStore) DestroySession(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

// --- Password file management ---

// HashPassword returns the SHA256 hex hash of a plaintext password.
func HashPassword(password string) string {
	h := sha256.Sum256([]byte(password))
	return hex.EncodeToString(h[:])
}

// ReadPasswordHash reads the stored password hash from disk.
func ReadPasswordHash(dataDir string) (string, error) {
	path := filepath.Join(dataDir, "password.sha256")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// WritePasswordHash writes a new password hash to disk.
func WritePasswordHash(dataDir, hash string) error {
	path := filepath.Join(dataDir, "password.sha256")
	return os.WriteFile(path, []byte(hash), 0600)
}

// PasswordFileExists checks if a password has been set.
func PasswordFileExists(dataDir string) bool {
	path := filepath.Join(dataDir, "password.sha256")
	_, err := os.Stat(path)
	return err == nil
}

// InitPassword creates the initial password file from the supplied password.
func InitPassword(dataDir, password string) error {
	return WritePasswordHash(dataDir, HashPassword(password))
}

// --- HTTP helpers ---

// SetSessionCookie sets the auth session cookie.
func SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// ClearSessionCookie removes the auth cookie.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:   "session",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
}

// GetSessionToken extracts the session token from the request cookie.
func GetSessionToken(r *http.Request) string {
	c, err := r.Cookie("session")
	if err != nil {
		return ""
	}
	return c.Value
}

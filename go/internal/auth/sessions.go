package auth

import (
	"sync"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
)

// AuthSession is an in-flight OAuth authorization session.
type AuthSession struct {
	ID          string
	DriverKey   string
	ProviderID  string // empty for new-provider flow
	StartResult StartResult
	Status      SessionStatus
	Credential  *storage.OAuthCredential
	Error       string
	CreatedAt   time.Time
	UpdatedAt   string
}

// SessionStore holds in-flight OAuth sessions (single-process; sticky-session
// for multi-replica — same caveat as Rust).
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*AuthSession
}

// NewSessionStore creates an empty session store.
func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: map[string]*AuthSession{}}
}

// Create stores a new session.
func (s *SessionStore) Create(sess *AuthSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess.CreatedAt = time.Now()
	sess.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	s.sessions[sess.ID] = sess
}

// Get retrieves a session by ID.
func (s *SessionStore) Get(id string) (*AuthSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	return sess, ok
}

// Update modifies an existing session.
func (s *SessionStore) Update(id string, fn func(*AuthSession)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[id]; ok {
		fn(sess)
		sess.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
}

// Delete removes a session.
func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

// Cleanup removes expired/terminal sessions older than the given duration.
func (s *SessionStore) Cleanup(maxAge time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	for id, sess := range s.sessions {
		if sess.CreatedAt.Before(cutoff) || sess.Status == StatusComplete || sess.Status == StatusError {
			delete(s.sessions, id)
		}
	}
}

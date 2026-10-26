package secrets

import (
	"context"
	"sync"
)

// SessionStore keeps credentials in one login lifetime. Closing it is a write
// barrier: a delayed token refresh cannot overwrite a later account's secrets.
type SessionStore struct {
	mu     sync.RWMutex
	parent Store
	values map[string]string
	closed bool
}

func NewSessionStore(parent Store, jwt string) *SessionStore {
	return &SessionStore{parent: parent, values: map[string]string{KeyJWT: jwt}}
}

func (s *SessionStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	clear(s.values)
}

func (s *SessionStore) Get(key string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return "", context.Canceled
	}
	value, ok := s.values[key]
	if !ok {
		return "", ErrNotFound
	}
	return value, nil
}

func (s *SessionStore) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return context.Canceled
	}
	if err := s.parent.Set(key, value); err != nil {
		return err
	}
	s.values[key] = value
	return nil
}

func (s *SessionStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return context.Canceled
	}
	if err := s.parent.Delete(key); err != nil {
		return err
	}
	delete(s.values, key)
	return nil
}

func (s *SessionStore) Exists(key string) (bool, error) {
	_, err := s.Get(key)
	if err == ErrNotFound {
		return false, nil
	}
	return err == nil, err
}

// RevokeAfter commits the next account's credentials under the old writer's
// barrier. Failed persistence leaves the old session usable; successful writes
// revoke delayed refreshes before releasing the barrier.
func (s *SessionStore) RevokeAfter(persist func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := persist(); err != nil {
		return err
	}
	s.closed = true
	clear(s.values)
	return nil
}

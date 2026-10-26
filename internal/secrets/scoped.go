package secrets

import (
	"crypto/sha256"
	"encoding/hex"
)

// ScopedStore keeps cloud credentials separate from application secrets and other accounts.
// The namespace is opaque; neither addresses nor user names become secret reference names.
type ScopedStore struct {
	parent Store
	prefix string
}

func NewScopedStore(parent Store, scope string) *ScopedStore {
	sum := sha256.Sum256([]byte(scope))
	return &ScopedStore{parent: parent, prefix: KeyPrefix + "account." + hex.EncodeToString(sum[:]) + "."}
}
func (s *ScopedStore) Set(key, value string) error     { return s.parent.Set(s.prefix+key, value) }
func (s *ScopedStore) Get(key string) (string, error)  { return s.parent.Get(s.prefix + key) }
func (s *ScopedStore) Delete(key string) error         { return s.parent.Delete(s.prefix + key) }
func (s *ScopedStore) Exists(key string) (bool, error) { return s.parent.Exists(s.prefix + key) }

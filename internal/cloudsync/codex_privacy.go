package cloudsync

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"goteams-client/internal/cloud"
	"goteams-client/internal/codexreply"
	"goteams-client/internal/secrets"
)

const codexEnvelope = "tb:codex:v1:"
const codexWithheld = "[TeamBuddy:private-content-omitted]"
const codexPrivacyVersion = 1
const codexExplicitVersion = 2

// Capture may run inside a switch/delete transaction, independently of the logged-in pusher.
var codexKeys struct {
	sync.Mutex
	store secrets.Store
}

func SetCodexPrivacyStore(s secrets.Store) {
	codexKeys.Lock()
	defer codexKeys.Unlock()
	codexKeys.store = s
}

var ErrCodexPrivacyKey = errors.New("codex_reply_key_error")
var ErrCodexPrivacySize = errors.New("codex_reply_size_error")

func codexAEAD(source, user string) (cipher.AEAD, error) {
	codexKeys.Lock()
	defer codexKeys.Unlock()
	unavailable := ErrCodexPrivacyKey
	if codexKeys.store == nil || source == "" || user == "" {
		return nil, unavailable
	}
	scope, _ := json.Marshal([]string{"codex-reply/v1", source, user})
	store := secrets.NewScopedStore(codexKeys.store, string(scope))
	const name = "aes256gcm.v1"
	encoded, err := store.Get(name)
	if errors.Is(err, secrets.ErrNotFound) {
		key := make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, unavailable
		}
		encoded = base64.StdEncoding.EncodeToString(key)
		if err = store.Set(name, encoded); err != nil {
			return nil, unavailable
		}
	} else if err != nil {
		return nil, unavailable
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, unavailable
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, unavailable
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, unavailable
	}
	return aead, nil
}

func isCodexVibe(p *cloud.PipelineTaskSessionSync) bool {
	return p.CliType == "codex" && p.StepKey == "vibe_coding"
}

// Old outbox payloads were already size-limited. A missing reply marker there cannot
// establish that the conversation was public, so protect every legacy Codex Vibe thread.
func protectCodexSnapshot(s *executionSnapshot, source, user string, legacy bool, explicitThread string) error {
	var aead cipher.AEAD
	for i := range s.Steps {
		p := &s.Steps[i].Payload
		if !isCodexVibe(p) {
			continue
		}
		s.CodexPrivacy = codexPrivacyVersion
		protect := map[string]bool{}
		private := map[string]bool{}
		replies := make([]*codexreply.Reply, len(p.Conversation.Rounds))
		for j, r := range p.Conversation.Rounds {
			if !legacy && explicitThread != "" && r.SessionUUID == explicitThread {
				s.CodexPrivacy = codexExplicitVersion
				if reply := codexreply.Parse(r.UserMessage, true); reply != nil {
					p.Conversation.Rounds[j].UserMessage = reply.Text(codexWithheld, codexWithheld)
				}
				continue
			}
			reply := codexreply.Parse(r.UserMessage, false)
			replies[j] = reply
			if legacy || reply != nil {
				protect[r.SessionUUID] = true
			}
			if reply != nil {
				for _, item := range reply.Items {
					if item.LocalOnly {
						private[r.SessionUUID] = true
					}
				}
			}
			if reply != nil && reply.Unavailable || legacy && len(r.UserMessage) >= cloudMessageMaxBytes-3 {
				private[r.SessionUUID] = true
			}
		}
		if len(protect) == 0 {
			fitSessionSyncPayload(p)
			continue
		}
		seal := func(value *string, field, thread string, limit int) error {
			if *value == "" {
				return nil
			}
			var err error
			if aead == nil {
				aead, err = codexAEAD(source, user)
				if err != nil {
					return err
				}
			}
			if len(codexEnvelope)+base64.RawURLEncoding.EncodedLen(len(*value)+aead.Overhead()) > limit {
				return ErrCodexPrivacySize
			}
			aad, _ := json.Marshal([]any{codexEnvelope, source, user, p.LocalTaskUUID, p.ExecutionIndex, thread, field})
			data := aead.Seal(nil, nil, []byte(*value), aad)
			*value = codexEnvelope + base64.RawURLEncoding.EncodeToString(data)
			return nil
		}
		// Vibe projections do not use CLI prompt/error/event fields. Don't transport
		// stale values from such fields when upgrading an older protected snapshot.
		p.PromptSnapshot = ""
		p.ErrorMessage = ""
		s.Task.ExecutionSummary = ""
		for j := range s.Task.Steps {
			st := &s.Task.Steps[j]
			st.PromptSummary = ""
			st.OutputSummary = ""
			st.ErrorSummary = ""
		}
		finalThread, finalField := "", ""
		for j := range p.Conversation.Rounds {
			r := &p.Conversation.Rounds[j]
			if len(r.AssistantMessages) > 0 {
				finalThread = r.SessionUUID
				finalField = fmt.Sprintf("r%d/a%d", j, len(r.AssistantMessages)-1)
			}
			if !protect[r.SessionUUID] {
				continue
			}
			r.ErrorSummary = ""
			r.Events = nil
			if private[r.SessionUUID] && r.UserMessage != "" {
				r.UserMessage = codexWithheld
			} else if replies[j] != nil {
				r.UserMessage = replies[j].Stored()
			}
			if err := seal(&r.UserMessage, fmt.Sprintf("r%d/u", j), r.SessionUUID, cloudMessageMaxBytes); err != nil {
				return err
			}
			for k := range r.AssistantMessages {
				msg := &r.AssistantMessages[k].Content
				if private[r.SessionUUID] && *msg != "" {
					*msg = codexWithheld
				}
				if err := seal(msg, fmt.Sprintf("r%d/a%d", j, k), r.SessionUUID, cloudMessageMaxBytes); err != nil {
					return err
				}
			}
		}
		// The final result is a duplicate of the last assistant message. A reference
		// avoids both a plaintext copy and a second, smaller ciphertext size limit.
		if p.FinalResult != "" && (protect[finalThread] || finalField == "") {
			p.FinalResult = codexWithheld
			if finalField != "" {
				p.FinalResult = codexEnvelope + "ref:" + finalField
			}
		}
		fitSessionSyncPayload(p)
	}
	return nil
}

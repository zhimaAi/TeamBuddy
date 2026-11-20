// Package codexreply projects Codex App question replies without consulting transcripts.
package codexreply

import (
	"encoding/json"
	"regexp"
	"strings"

	"goteams-client/internal/redact"
)

const (
	open         = "<send_user_message_question_reply>"
	closeTag     = "</send_user_message_question_reply>"
	marker       = "send_user_message_question_reply"
	HiddenResult = "[TeamBuddy:private-reply]"
)

type Item struct {
	Question  string `json:"question"`
	Answer    string `json:"answer"`
	Hidden    bool   `json:"isSecret,omitempty"`
	LocalOnly bool   `json:"localOnly,omitempty"`
}
type Reply struct {
	Prefix      string `json:"prefix,omitempty"`
	Items       []Item `json:"items,omitempty"`
	Unavailable bool   `json:"unavailable,omitempty"`
}

// Historical bindings keep the display semantics of the original trial.
var credentialQuestion = regexp.MustCompile(`(?i)(password|passphrase|secret|token|api[ _-]?key|private[ _-]?key|credential|密码|口令|密钥|令牌|验证码|凭据)`)
var opaqueAnswer = regexp.MustCompile(`^[a-zA-Z0-9_+/=.\-]{24,}$`)

// ExplicitThread returns the one thread whose binding opted into explicit fields.
func ExplicitThread(raw string) string {
	var binding struct {
		Type     string
		ThreadID string `json:"thread_id"`
		Owner    string `json:"reporting_owner"`
		Explicit bool   `json:"explicit_privacy"`
	}
	if json.Unmarshal([]byte(raw), &binding) == nil && binding.Type == "codex" && binding.Owner == "hooks" && binding.Explicit {
		return binding.ThreadID
	}
	return ""
}

// Parse reads the native form. Only the old trial inferred private rows from their contents.
func Parse(raw string, explicit bool) *Reply {
	if !strings.Contains(raw, "<"+marker) && !strings.Contains(raw, "</"+marker) {
		return nil
	}
	prefix, tail, ok := strings.Cut(raw, open)
	invalid := &Reply{Unavailable: true}
	if !ok || strings.Contains(prefix, marker) || len(raw) > 2<<20 {
		return invalid
	}
	body, rest, ok := strings.Cut(tail, closeTag)
	if !ok || strings.TrimSpace(rest) != "" {
		return invalid
	}
	body = strings.TrimSpace(body)
	if strings.HasPrefix(body, "{") {
		body = "[" + body + "]"
	}
	var rows []struct {
		Question  *string `json:"question"`
		Answer    *string `json:"answer"`
		Secret    bool    `json:"isSecret"`
		LocalOnly bool    `json:"localOnly"`
	}
	if json.Unmarshal([]byte(body), &rows) != nil || len(rows) == 0 || len(rows) > 64 {
		return invalid
	}
	r := &Reply{Prefix: strings.TrimSpace(prefix)}
	for _, row := range rows {
		if row.Question == nil || row.Answer == nil {
			return invalid
		}
		r.Items = append(r.Items, Item{*row.Question, *row.Answer, row.Secret || row.LocalOnly, row.LocalOnly})
	}
	if !explicit {
		r.applyLegacyPrivacy()
	}
	for i := range r.Items {
		if r.Items[i].Hidden {
			r.Items[i].Question = ""
			r.Items[i].Answer = ""
		}
	}
	return r
}

// Only records from the earlier trial used content-based masking.
func (r *Reply) applyLegacyPrivacy() {
	var discarded []string
	for i := range r.Items {
		row := &r.Items[i]
		_, qSecret := redact.Redact(row.Question)
		_, aSecret := redact.Redact(row.Answer)
		row.Hidden = row.Hidden || qSecret || aSecret || credentialQuestion.MatchString(row.Question) || opaqueAnswer.MatchString(strings.TrimSpace(row.Answer))
		if row.Hidden {
			r.Prefix = ""
			if row.Answer != "" {
				discarded = append(discarded, row.Answer)
			}
		}
	}
	for i, v := range r.Items {
		if v.Hidden {
			continue
		}
		for _, s := range discarded {
			if strings.Contains(v.Question, s) || strings.Contains(v.Answer, s) {
				r.Items[i].Hidden = true
				break
			}
		}
	}
}

func (r *Reply) Private() bool {
	if r == nil {
		return false
	}
	if r.Unavailable {
		return true
	}
	for _, v := range r.Items {
		if v.Hidden {
			return true
		}
	}
	return false
}

// Stored drops unknown fields, call IDs and hidden originals before the callback spool.
func (r *Reply) Stored() string {
	if r.Unavailable {
		return open + "{}" + closeTag
	}
	data, _ := json.Marshal(r.Items)
	prefix := r.Prefix
	if prefix != "" {
		prefix += "\n"
	}
	return prefix + open + string(data) + closeTag
}

func (r *Reply) Text(hidden, unavailable string) string {
	if r.Unavailable {
		return unavailable
	}
	lines := []string{}
	if r.Prefix != "" {
		lines = append(lines, r.Prefix)
	}
	for _, v := range r.Items {
		if v.Hidden {
			lines = append(lines, hidden)
		} else {
			lines = append(lines, v.Question+"\n"+v.Answer)
		}
	}
	return strings.Join(lines, "\n\n")
}

// View walks records in their stored order, hiding an old final reply that may echo a private answer.
type View struct {
	private             map[string]bool
	hidden, unavailable string
}

func NewView(hidden, unavailable string) View { return View{map[string]bool{}, hidden, unavailable} }
func (v View) Project(thread string, prompt, result *string, explicit bool) *Reply {
	r := Parse(*prompt, explicit)
	if *prompt != "" {
		v.private[thread] = !explicit && r.Private()
	}
	if r != nil {
		*prompt = r.Text(v.hidden, v.unavailable)
	}
	if *result != "" && (v.private[thread] || *result == HiddenResult) {
		*result = v.hidden
	}
	return r
}

package executor

import "strings"

// SplitCLIModelSelector validates Cursor's native model[key=value,...] syntax
// without discarding unrelated context/fast parameters when replacing effort.
func SplitCLIModelSelector(model string) (string, []string, error) {
	base, tail, found := strings.Cut(model, "[")
	if base == "" || strings.TrimSpace(base) != base || strings.ContainsAny(base, "]\r\n\x00") {
		return "", nil, ErrThinkingInvalid
	}
	if !found {
		return base, nil, nil
	}
	if !strings.HasSuffix(tail, "]") {
		return "", nil, ErrThinkingInvalid
	}
	tail = strings.TrimSuffix(tail, "]")
	if tail == "" {
		return "", nil, ErrThinkingInvalid
	}
	pairs := strings.Split(tail, ",")
	seen := make(map[string]bool, len(pairs))
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || !thinkingToken.MatchString(key) || !thinkingToken.MatchString(value) || seen[key] {
			return "", nil, ErrThinkingInvalid
		}
		seen[key] = true
	}
	return base, pairs, nil
}

package localserver

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// remoteNLP 命名空间的辅助函数（避免与 wechat_dispatch.go 顶层的同名函数冲突）。
// 命名规范：全部以 remote 开头。

// remoteContainsForceKeyword 判断消息是否含「强制 / 直接 / -f / force」等跳过二次确认的关键词。
func remoteContainsForceKeyword(content string) bool {
	lower := strings.ToLower(content)
	for _, token := range strings.Fields(lower) {
		if token == "-f" || token == "-force" || token == "--force" {
			return true
		}
	}
	for _, p := range []string{"强制", "直接取消", "直接执行", "直接完成", "直接恢复", "跳过确认", "无需确认"} {
		if strings.Contains(content, p) {
			return true
		}
	}
	for _, kw := range []string{"force", "directly", "skip confirm", "no confirm", "without confirm"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// remoteWithinIntentLengthLimit 决定自然语言意图匹配的长度上限。
func remoteWithinIntentLengthLimit(content string, hasID bool) bool {
	runeCount := len([]rune(content))
	byteCount := len(content)
	maxRune, maxByte := 15, 60
	if hasID {
		maxRune, maxByte = 30, 120
	}
	if runeCount == byteCount {
		return byteCount <= maxByte
	}
	return runeCount <= maxRune && byteCount <= maxByte
}

// remoteExtractTaskID 抽取消息中出现的任务 ID 数字。
func remoteExtractTaskID(content string) string {
	if strings.HasSuffix(content, "号") {
		rest := strings.TrimSpace(strings.TrimSuffix(content, "号"))
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "任务"))
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "Task"))
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "task"))
		if _, err := strconv.ParseInt(rest, 10, 64); err == nil {
			return rest
		}
	}
	re := regexp.MustCompile(`\d+`)
	match := re.FindString(content)
	if match != "" {
		return match
	}
	return ""
}

// remoteMatchTaskIDAfterKeyword 在关键词后提取任务 ID。
func remoteMatchTaskIDAfterKeyword(content string, keywords []string) (bool, string) {
	for _, kw := range keywords {
		if strings.HasPrefix(content, kw) {
			rest := strings.TrimSpace(strings.TrimPrefix(content, kw))
			if rest == "" {
				return true, ""
			}
			if strings.HasPrefix(rest, "任务") {
				rest = strings.TrimSpace(strings.TrimPrefix(rest, "任务"))
			}
			if rest != "" {
				return true, rest
			}
		}
	}
	return false, ""
}

// remoteMatchBareTaskID 纯任务 ID 形式。
func remoteMatchBareTaskID(content string) (bool, string) {
	content = strings.TrimSpace(content)
	for _, prefix := range []string{"任务", "task", "Task"} {
		if strings.HasPrefix(content, prefix) {
			rest := strings.TrimSpace(strings.TrimPrefix(content, prefix))
			if rest == "" {
				return false, ""
			}
			rest = strings.TrimSuffix(rest, "号")
			if _, err := strconv.ParseInt(rest, 10, 64); err == nil {
				return true, rest
			}
			return false, ""
		}
	}
	if strings.HasSuffix(content, "号") {
		rest := strings.TrimSpace(strings.TrimSuffix(content, "号"))
		if _, err := strconv.ParseInt(rest, 10, 64); err == nil {
			return true, rest
		}
	}
	return false, ""
}

// remoteNormalizeCommandAlias 命令别名标准化。
func remoteNormalizeCommandAlias(cmd string) string {
	switch cmd {
	case "/teamstask":
		return "/tt"
	case "/projects":
		return "/pro"
	case "/cli-discovery":
		return "/clis"
	case "/cli-models":
		return "/clim"
	case "/pipelines":
		return "/pls"
	case "/expert-groups":
		return "/egs"
	case "/vibecodinglist":
		return "/vcl"
	case "/codex-capability":
		return "/vcl"
	case "/switch-model":
		return "/swm"
	case "/switch-run":
		return "/swr"
	case "/switch-cli":
		return "/swcli"
	case "/task-info":
		return "/ti"
	case "/create-task":
		return "/ct"
	case "/create-team-task":
		return "/ctt"
	}
	return cmd
}

// remoteContainsAllKeyword 判断内容是否含「所有 / 全部 / all」这类"列全"信号。
// nlIntentListTasks 应当只由明确的"列全"语义触发，而不是任何「任务」字眼，
// 否则任务对话里随口提到「任务」就会被误识别为"列出所有任务"。
func remoteContainsAllKeyword(raw string) bool {
	lower := strings.ToLower(raw)
	if strings.Contains(raw, "所有") || strings.Contains(raw, "全部") {
		return true
	}
	for _, kw := range []string{"all tasks", "all the tasks", "all of the tasks", "every task", "every tasks", "all my tasks", "all of them", "everything"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// remoteHasWholeWord 判断 lower（已小写）是否含以空白/标点分隔的整词之一。
func remoteHasWholeWord(lower string, words ...string) bool {
	for _, tok := range strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		for _, w := range words {
			if tok == w {
				return true
			}
		}
	}
	return false
}

// remoteContainsAny 判断 lower（已小写）是否含任一关键词（子串匹配）。
func remoteContainsAny(lower string, subs ...string) bool {
	for _, s := range subs {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// remoteMatchEntityListingIntent 是 matchRound2Keywords 的细化分支。
func remoteMatchEntityListingIntent(lower string, conflictingAction bool, idText string) (string, bool) {
	return "", false
}

// remoteGreetingTrailingPunct / remoteGreetingParticles 用于问候语归一化，
// 让「你好！」「在吗呀」「hi!」这类带句尾标点或语气词的写法也能命中。
const (
	remoteGreetingTrailingPunct = " \t\r\n!！?？。.~～,，、;；:：…"
	// 注意：不要加入「吗」——否则「在吗」「有人吗」会被截成「在」「有人」而失配。
	remoteGreetingParticles = "啊呀哈哦嗯吧呢嘛啦咯喔"
)

// remoteGreetingWords 是与「帮助」等价的问候语集合：命中后与 `帮助` / `help` / `/help`
// 一致返回完整命令说明（remoteNLIntentHelp）。中文不做分词，按整串比对；英文短语含空格也按整串。
var remoteGreetingWords = []string{
	// 你好 及其变体
	"你好", "您好", "哈喽", "嗨",
	// 在吗 及其变体
	"在吗", "在么", "在不在", "有人吗", "有人么",
	// hello / hi 及其变体
	"hello", "hello there", "hi", "hi there", "hey",
	// are you there 及其变体
	"are you there", "are you online", "you there", "anyone there", "anybody there",
	"anyone home", "anybody home",
}

// remoteGreetingTokenSeqs 把 remoteGreetingWords 按空格拆成词序列，供分段覆盖匹配使用。
var remoteGreetingTokenSeqs = func() [][]string {
	seqs := make([][]string, 0, len(remoteGreetingWords))
	for _, w := range remoteGreetingWords {
		seqs = append(seqs, strings.Fields(w))
	}
	return seqs
}()

// remoteNormalizeGreeting 归一化问候语：小写、去句尾标点、去句尾语气词。
func remoteNormalizeGreeting(content string) string {
	trimmed := strings.ToLower(strings.TrimSpace(content))
	trimmed = strings.TrimRight(trimmed, remoteGreetingTrailingPunct)
	trimmed = strings.TrimRight(trimmed, remoteGreetingParticles)
	return strings.TrimSpace(trimmed)
}

// remoteIsGreetingWord 判断已归一化的文本是否恰为某个问候语。
func remoteIsGreetingWord(normalized string) bool {
	if normalized == "" {
		return false
	}
	for _, w := range remoteGreetingWords {
		if normalized == w {
			return true
		}
	}
	return false
}

// remoteIsGreeting 判断消息是否为问候语（你好 / 在吗 / hello / hi ...）。
// 用于 matchRound1Strict 的精确匹配：整串相等即命中。
func remoteIsGreeting(content string) bool {
	return remoteIsGreetingWord(remoteNormalizeGreeting(content))
}

// remoteContainsGreeting 是 matchRound2Keywords 用的宽松匹配，支持三类写法：
//   - 整串是问候语：「你好」「are you there」
//   - 分隔符连接多个问候语：「你好，在吗」「hello, are you there」
//   - 中文词中间被误加空格：「你 好」
//
// 刻意不做子串匹配：中文「你好厉害」、英文 this / which 里的 "hi" 都不应命中。
func remoteContainsGreeting(content string) bool {
	normalized := remoteNormalizeGreeting(content)
	if remoteIsGreetingWord(normalized) {
		return true
	}
	if normalized == "" {
		return false
	}
	tokens := strings.Fields(strings.Map(func(r rune) rune {
		if remoteIsGreetingSeparator(r) {
			return ' '
		}
		return r
	}, normalized))
	if len(tokens) == 0 {
		return false
	}
	// 去掉分隔符后整体命中（容忍「你 好」这类词内空格）。
	if remoteIsGreetingWord(strings.Join(tokens, "")) {
		return true
	}
	return remoteMatchGreetingTokens(tokens)
}

// remoteIsGreetingSeparator 判断是否为问候语之间的分隔符。
func remoteIsGreetingSeparator(r rune) bool {
	switch r {
	case ' ', '\t', '\r', '\n',
		'，', ',', '、', '；', ';', '：', ':', '。', '.', '！', '!', '？', '?', '…', '~', '～':
		return true
	}
	return false
}

// remoteMatchGreetingTokens 判断 token 序列能否被问候语词序列完整覆盖（逐位置贪心取最长匹配）。
// 例：["hello", "are", "you", "there"] → "hello" + "are you there"。
func remoteMatchGreetingTokens(tokens []string) bool {
	matched := false
	for i := 0; i < len(tokens); {
		longest := 0
		for _, seq := range remoteGreetingTokenSeqs {
			if len(seq) <= len(tokens)-i && len(seq) > longest && remoteTokensEqual(tokens[i:i+len(seq)], seq) {
				longest = len(seq)
			}
		}
		if longest == 0 {
			return false
		}
		matched = true
		i += longest
	}
	return matched
}

// remoteTokensEqual 比较两个 token 序列是否逐项相等。
func remoteTokensEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

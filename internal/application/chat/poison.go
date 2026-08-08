// poison.go — 记忆投毒检测：PII / 越狱关键词 / 可疑指令模式。
//
// 设计原则：
//   - 纯函数 + 大小写不敏感 + 编译期正则
//   - 三类返回值：safe / suspicious（拦截，不入 LTM）/ pii（拦截，且不回显）
//   - 调用方在写入前调 inspectMemoryContent；命中后只 log 不 panic
//
// 防御范围（举例，不穷尽）：
//   - 凭证类：密码 / token / api_key / 私钥 / 信用卡 / 身份证
//   - 越狱类：忽略之前指令 / ignore previous / "你现在是" / "system: ..."
//   - 角色劫持：要求 agent 长期改变身份或行为
//
// 误伤策略：宁可漏检不可错杀正常对话——所以这里只截"高置信"模式，
// 模糊判断留给 LLM 守门员（可后续接入）。
package chat

import (
	"regexp"
	"strings"
)

// MemRisk 标记记忆候选的安全等级
type MemRisk string

const (
	MemRiskSafe      MemRisk = "safe"      // 通过
	MemRiskPII       MemRisk = "pii"       // 含敏感个人信息：禁止入库且不回显
	MemRiskInjection MemRisk = "injection" // 含越狱/角色劫持模式：禁止入库
	MemRiskEphemeral MemRisk = "ephemeral" // 临时性内容（"今天/刚才"）：跳过入库
)

// MemInspection 是检测结果，附原因便于审计日志
type MemInspection struct {
	Risk    MemRisk
	Reason  string // 命中的具体规则名（用于日志和 audit）
	Matched string // 命中的子串片段（截断到 40 字符，避免日志泄漏）
}

// Safe 返回是否允许写入 LTM
func (i MemInspection) Safe() bool { return i.Risk == MemRiskSafe }

// ─────────────────────────────── 规则集 ────────────────────────────────

// piiPatterns 高置信 PII / 凭证模式。命中即视为禁止入库。
//
// 注：正则按"出现关键字 + 后跟值"风格匹配，避免把"密码学课程"误判为密码。
var piiPatterns = []namedPattern{
	{"password_keyword", regexp.MustCompile(`(?i)(密\s*码|password|passwd|passphrase)\s*(是|为|=|:)\s*\S{3,}`)},
	{"api_key", regexp.MustCompile(`(?i)(api[\s_\-]?key|access[\s_\-]?key|secret[\s_\-]?key)\s*(是|为|=|:)\s*\S{6,}`)},
	{"token", regexp.MustCompile(`(?i)(bearer|jwt|access[\s_\-]?token|refresh[\s_\-]?token)\s*(是|为|=|:)?\s*[\w\-\.]{20,}`)},
	{"private_key_block", regexp.MustCompile(`-----BEGIN\s+(RSA|OPENSSH|DSA|EC|PRIVATE)\s+PRIVATE\s+KEY-----`)},
	{"id_card_cn", regexp.MustCompile(`\b\d{17}[\dXx]\b`)},                // 中国身份证 18 位
	{"credit_card", regexp.MustCompile(`\b(?:\d[ -]*?){13,19}\b`)},        // 13-19 位连续/分隔数字
	{"aws_key", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},                   // AWS Access Key ID
	{"github_token", regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36,255}`)}, // GitHub PAT
}

// injectionPatterns 越狱/角色劫持模式。
//
// 这些是"用户意图让 agent 长期改变行为"的信号，不应进 LTM——一旦进了
// LTM，每次召回都会注入 system prompt，等于持续越狱。
var injectionPatterns = []namedPattern{
	{"ignore_previous", regexp.MustCompile(`(?i)(忽略|无视|disregard|ignore)\s*(之前|前面|所有|previous|all\s+prior|above)\s*(指令|内容|规则|instructions?|rules?)?`)},
	{"role_override_zh", regexp.MustCompile(`你\s*(现在|从现在起|从此|以后)\s*(是|扮演|作为|当)`)},
	{"role_override_en", regexp.MustCompile(`(?i)you\s+are\s+now\s+(a|an|the)\s+`)},
	{"system_role_inject", regexp.MustCompile(`(?i)^\s*(system|assistant|user)\s*[:：]\s*`)},
	{"jailbreak_prompt", regexp.MustCompile(`(?i)(DAN|do\s+anything\s+now|developer\s+mode|越狱)`)},
	{"persistent_command", regexp.MustCompile(`(?i)(永远|从今以后|每次|总是|always|forever|from\s+now\s+on)\s*(回复|回答|说|拒绝|reply|answer|say|refuse)`)},
	{"memory_injection", regexp.MustCompile(`(?i)(请\s*)?(记住|牢记|永远记住|remember\s+(this|that|always))[：:、，,]`)},
}

// ephemeralPatterns 临时性内容信号词。命中表明这条信息时效短，不值得入 LTM。
//
// 不是"危险"，只是"价值低"——如果未来加 importance 分级，可以改为降权而非完全跳过。
var ephemeralPatterns = []namedPattern{
	{"now_words", regexp.MustCompile(`(今天|今晚|刚才|这次|此刻|现在|马上|稍后|just\s+now|right\s+now|today|tonight)`)},
	{"weather_smalltalk", regexp.MustCompile(`(天气|温度|气温).{0,10}(怎么样|如何|不错|很好|很差)`)},
}

type namedPattern struct {
	name string
	re   *regexp.Regexp
}

// ─────────────────────────────── 主入口 ────────────────────────────────

// inspectMemoryContent 检测一段拟写入 LTM 的文本是否安全。
//
// 检测顺序（先严后宽）：PII → Injection → Ephemeral → Safe。
// 命中即返回，不做累计——一段文本同时命中多类时按最高风险记录。
func inspectMemoryContent(content string) MemInspection {
	if strings.TrimSpace(content) == "" {
		return MemInspection{Risk: MemRiskSafe}
	}

	if r := matchAny(content, piiPatterns); r.name != "" {
		return MemInspection{Risk: MemRiskPII, Reason: r.name, Matched: r.snippet}
	}
	if r := matchAny(content, injectionPatterns); r.name != "" {
		return MemInspection{Risk: MemRiskInjection, Reason: r.name, Matched: r.snippet}
	}
	if r := matchAny(content, ephemeralPatterns); r.name != "" {
		return MemInspection{Risk: MemRiskEphemeral, Reason: r.name, Matched: r.snippet}
	}
	return MemInspection{Risk: MemRiskSafe}
}

type matchHit struct {
	name    string
	snippet string
}

// matchAny 返回首个命中的规则名 + 匹配片段（截断到 40 字符）
func matchAny(content string, patterns []namedPattern) matchHit {
	for _, p := range patterns {
		if loc := p.re.FindStringIndex(content); loc != nil {
			snip := content[loc[0]:loc[1]]
			runes := []rune(snip)
			if len(runes) > 40 {
				snip = string(runes[:40]) + "…"
			}
			return matchHit{name: p.name, snippet: snip}
		}
	}
	return matchHit{}
}

// inspectKVPair 在偏好 k-v 提取场景下使用：把 key 和 value 拼成 "key=value"
// 形式后整体检测——既能命中 "password=hunter2" 这种被拆到 k 和 v 的攻击，
// 也能正常通过 "favorite_drink=coffee" 这种安全 k-v。
//
// 同时会单独检查 key 和 value，捕获只在某一边出现的越狱片段。
func inspectKVPair(key, value string) MemInspection {
	if r := inspectMemoryContent(key + "=" + value); !r.Safe() {
		return r
	}
	if r := inspectMemoryContent(key); !r.Safe() {
		return r
	}
	if r := inspectMemoryContent(value); !r.Safe() {
		return r
	}
	return MemInspection{Risk: MemRiskSafe}
}

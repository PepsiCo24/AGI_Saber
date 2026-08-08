// Package rag 文本分割器。
//
// 提供两种实现：
//   - TextSplitter         旧的字符滑窗切分（保留以兼容旧调用方）
//   - RecursiveSplitter    新的递归切分 + Markdown 感知 + 句子边界感知
//
// 切分策略说明（RecursiveSplitter）：
//
//	递归地用一组优先级递减的分隔符把文本切成不超过 chunkSize 的片段。
//	先用最强的分隔符（段落 / Markdown 标题）切，子片段若仍过长则降一级
//	（句号 → 逗号 → 空格 → 字符）继续切，直到所有片段都符合大小约束。
//
//	相比定长滑窗的优势：
//	  - 不会把句子 / 段落 / 标题 / 代码块拦腰斩断
//	  - 同一逻辑单元尽量保持完整，召回时 LLM 不会拿到半句话
//	  - 对中英混排、Markdown 文档都友好
//
//	重叠策略：在拼接 chunk 时按 `overlap` 字符数把上一片段的末尾
//	附加到下一片段开头，缓解硬切边界处的语义断裂。
package rag

import (
	"regexp"
	"strings"
)

// Splitter 是文本切分器统一接口
type Splitter interface {
	Split(text string) []Chunk
}

// ─────────────────────────────── 旧实现（保留） ────────────────────────────

// TextSplitter 按字符窗口将长文本切成有重叠的 Chunk
type TextSplitter struct {
	chunkSize int
	overlap   int
}

// NewTextSplitter 创建定长滑窗切分器（保留用于兼容老路径）
func NewTextSplitter(chunkSize, overlap int) *TextSplitter {
	return &TextSplitter{chunkSize: chunkSize, overlap: overlap}
}

// Split 将文本切分为 Chunk 列表（Unicode 安全）
func (s *TextSplitter) Split(text string) []Chunk {
	var chunks []Chunk
	step := s.chunkSize - s.overlap
	if step <= 0 {
		step = s.chunkSize
	}
	runes := []rune(text)
	id := 0
	for i := 0; i < len(runes); i += step {
		end := i + s.chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, Chunk{ID: id, Content: string(runes[i:end])})
		id++
		if end >= len(runes) {
			break
		}
	}
	return chunks
}

// ─────────────────────────────── 递归切分实现 ─────────────────────────────

// 默认分隔符列表，按"语义强度"从强到弱排列。
// 越靠前的分隔符切出的片段语义越完整，因此优先尝试。
//
// Markdown 标题（^#+ ）作为最强分隔符；其后是空行（段落）、单换行、
// 中英文句末标点、英文句点、逗号、空格，最后退化到字符级别。
var defaultSeparators = []string{
	"\n## ", "\n### ", "\n#### ", "\n# ", // Markdown 标题（保留 # 给后续判断）
	"\n\n",        // 段落
	"\n",          // 行
	"。", "！", "？", // 中文句末
	". ", "! ", "? ", // 英文句末（带空格避免误切小数 / 缩写）
	"；", "; ",
	"，", ", ",
	" ",
	"",
}

// codeFenceRe 用于检测代码块开闭。出现在代码块内部的换行不应被当作分隔符。
var codeFenceRe = regexp.MustCompile("(?m)^```")

// RecursiveSplitter 是递归切分器
type RecursiveSplitter struct {
	chunkSize  int
	overlap    int
	separators []string
}

// NewRecursiveSplitter 创建递归切分器。separators 传 nil 时使用 defaultSeparators
func NewRecursiveSplitter(chunkSize, overlap int, separators []string) *RecursiveSplitter {
	if separators == nil {
		separators = defaultSeparators
	}
	return &RecursiveSplitter{
		chunkSize:  chunkSize,
		overlap:    overlap,
		separators: separators,
	}
}

// Split 将文本切分为有序 Chunk 列表
func (s *RecursiveSplitter) Split(text string) []Chunk {
	// 先把成对出现的代码块识别出来作为不可切分的原子单元
	atoms := s.protectCodeBlocks(text)
	var pieces []string
	for _, a := range atoms {
		if a.atomic {
			pieces = append(pieces, a.text)
			continue
		}
		pieces = append(pieces, s.recursiveSplit(a.text, s.separators)...)
	}

	// 把过短的片段贪心合并到 chunkSize 附近，附带 overlap
	merged := s.merge(pieces)

	chunks := make([]Chunk, len(merged))
	for i, c := range merged {
		chunks[i] = Chunk{ID: i, Content: c}
	}
	return chunks
}

type atom struct {
	text   string
	atomic bool // true 表示不可再切（如代码块）
}

// protectCodeBlocks 把成对的 ```...``` 代码块识别出来作为原子单元
// 即使代码块超长也不切分，避免破坏代码语义；调用方可在 prompt 中
// 提示 LLM 注意单块过长的情况
func (s *RecursiveSplitter) protectCodeBlocks(text string) []atom {
	idxs := codeFenceRe.FindAllStringIndex(text, -1)
	if len(idxs) < 2 {
		return []atom{{text: text}}
	}
	var atoms []atom
	cursor := 0
	// 配对处理 fence：偶数下标为开 fence，奇数为闭 fence
	for i := 0; i+1 < len(idxs); i += 2 {
		open := idxs[i][0]
		close := idxs[i+1][1]
		if open > cursor {
			atoms = append(atoms, atom{text: text[cursor:open]})
		}
		atoms = append(atoms, atom{text: text[open:close], atomic: true})
		cursor = close
	}
	if cursor < len(text) {
		atoms = append(atoms, atom{text: text[cursor:]})
	}
	return atoms
}

// recursiveSplit 递归切分：用当前最强分隔符切，子片段若仍超长则降级再切
func (s *RecursiveSplitter) recursiveSplit(text string, seps []string) []string {
	if runeLen(text) <= s.chunkSize {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []string{text}
	}
	if len(seps) == 0 {
		// 已无可用分隔符，按 rune 硬切
		return hardSplitByRune(text, s.chunkSize)
	}

	sep := seps[0]
	rest := seps[1:]

	// 空字符串分隔符的语义是"按 rune 硬切"
	if sep == "" {
		return hardSplitByRune(text, s.chunkSize)
	}

	parts := splitKeepingSep(text, sep)
	var out []string
	for _, p := range parts {
		if runeLen(p) <= s.chunkSize {
			if strings.TrimSpace(p) != "" {
				out = append(out, p)
			}
			continue
		}
		// 还超长 → 降级用更弱的分隔符继续切
		out = append(out, s.recursiveSplit(p, rest)...)
	}
	return out
}

// splitKeepingSep 用 sep 切分，但把 sep 保留在切出的下一段开头，
// 这样 Markdown 标题 / 句末标点不会丢失
func splitKeepingSep(text, sep string) []string {
	if sep == "" {
		return []string{text}
	}
	parts := strings.Split(text, sep)
	if len(parts) <= 1 {
		return parts
	}
	out := make([]string, 0, len(parts))
	out = append(out, parts[0])
	for i := 1; i < len(parts); i++ {
		out = append(out, sep+parts[i])
	}
	return out
}

// merge 把过短片段贪心合并到接近 chunkSize 的目标，并在每个 chunk 末尾附 overlap 个字符到下一 chunk 开头
func (s *RecursiveSplitter) merge(pieces []string) []string {
	var merged []string
	var buf strings.Builder
	bufLen := 0
	flush := func() {
		if bufLen > 0 {
			merged = append(merged, buf.String())
			buf.Reset()
			bufLen = 0
		}
	}
	for _, p := range pieces {
		pl := runeLen(p)
		if bufLen == 0 {
			buf.WriteString(p)
			bufLen = pl
			continue
		}
		if bufLen+pl <= s.chunkSize {
			buf.WriteString(p)
			bufLen += pl
			continue
		}
		flush()
		buf.WriteString(p)
		bufLen = pl
	}
	flush()

	// 应用重叠：把上一段尾部 overlap 字符附到下一段开头
	if s.overlap > 0 && len(merged) > 1 {
		out := make([]string, len(merged))
		out[0] = merged[0]
		for i := 1; i < len(merged); i++ {
			prev := merged[i-1]
			tail := tailRunes(prev, s.overlap)
			out[i] = tail + merged[i]
		}
		return out
	}
	return merged
}

// hardSplitByRune 按 rune 等长硬切，作为递归终止后兜底
func hardSplitByRune(text string, size int) []string {
	runes := []rune(text)
	var out []string
	for i := 0; i < len(runes); i += size {
		end := i + size
		if end > len(runes) {
			end = len(runes)
		}
		out = append(out, string(runes[i:end]))
	}
	return out
}

func runeLen(s string) int { return len([]rune(s)) }

func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

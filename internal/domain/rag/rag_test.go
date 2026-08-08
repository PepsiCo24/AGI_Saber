package rag

import (
	"strings"
	"testing"
)

func TestRecursiveSplitter_RespectsParagraphBoundary(t *testing.T) {
	s := NewRecursiveSplitter(50, 0, nil)
	text := "第一段内容比较短。\n\n第二段开头，这里写一些东西，凑够长度。\n\n第三段。"
	chunks := s.Split(text)
	if len(chunks) == 0 {
		t.Fatal("expected non-empty chunks")
	}
	// 不应该把任何"第N段"标识切到中间
	for _, c := range chunks {
		if strings.Contains(c.Content, "第二段开") && !strings.Contains(c.Content, "第二段开头") {
			t.Errorf("split inside a sentence: %q", c.Content)
		}
	}
}

func TestRecursiveSplitter_PreservesCodeBlock(t *testing.T) {
	s := NewRecursiveSplitter(30, 0, nil)
	text := "前文说明。\n\n```go\nfunc Foo() {\n  println(\"hello\")\n}\n```\n\n后文总结。"
	chunks := s.Split(text)
	// 至少有一个 chunk 包含完整的代码块
	foundFull := false
	for _, c := range chunks {
		if strings.Contains(c.Content, "func Foo()") && strings.Contains(c.Content, "```") {
			foundFull = true
		}
	}
	if !foundFull {
		t.Errorf("code block was split; chunks=%v", chunks)
	}
}

func TestRecursiveSplitter_SizeBounded(t *testing.T) {
	s := NewRecursiveSplitter(40, 5, nil)
	text := strings.Repeat("中文内容反复出现，", 50)
	chunks := s.Split(text)
	for i, c := range chunks {
		// overlap 会让某些 chunk 略超 chunkSize，但不应超太多
		if runeLen(c.Content) > 60 {
			t.Errorf("chunk[%d] too long: len=%d", i, runeLen(c.Content))
		}
	}
}

func TestRecursiveSplitter_OverlapApplied(t *testing.T) {
	s := NewRecursiveSplitter(20, 5, nil)
	text := "AAAAAAAAAA。BBBBBBBBBB。CCCCCCCCCC。DDDDDDDDDD。"
	chunks := s.Split(text)
	if len(chunks) < 2 {
		t.Fatalf("expected ≥2 chunks, got %d", len(chunks))
	}
	// 第二段开头应该带上一段尾部 5 字符
	prevTail := tailRunes(chunks[0].Content, 5)
	if !strings.HasPrefix(chunks[1].Content, prevTail) {
		t.Errorf("overlap missing: prev tail=%q, next prefix=%q",
			prevTail, string([]rune(chunks[1].Content)[:5]))
	}
}

func TestParseRewriteJSON_HandlesCodeFence(t *testing.T) {
	raw := "```json\n{\"queries\": [\"a\", \"b\", \"c\"]}\n```"
	out := parseRewriteJSON(raw)
	if len(out) != 3 || out[0] != "a" {
		t.Errorf("unexpected parse: %v", out)
	}
}

func TestParseRerankJSON_HandlesPlain(t *testing.T) {
	raw := `{"scores":[{"idx":0,"score":9},{"idx":1,"score":3}]}`
	out := parseRerankJSON(raw)
	if len(out) != 2 || out[0].Score != 9 || out[1].Idx != 1 {
		t.Errorf("unexpected parse: %v", out)
	}
}

func TestLLMRewriter_FallbackOnNoLLM(t *testing.T) {
	r := NewLLMRewriter(nil, 3)
	got := r.Rewrite("你好", nil)
	if len(got) != 1 || got[0] != "你好" {
		t.Errorf("expected fallback to original, got %v", got)
	}
}

func TestLLMReranker_FallbackOnNoLLM(t *testing.T) {
	r := NewLLMReranker(nil, 0)
	in := []HybridResult{
		{Chunk: Chunk{Content: "a"}, Score: 0.5},
		{Chunk: Chunk{Content: "b"}, Score: 0.4},
		{Chunk: Chunk{Content: "c"}, Score: 0.3},
	}
	got := r.Rerank("q", in, 2)
	if len(got) != 2 || got[0].Chunk.Content != "a" {
		t.Errorf("expected truncate to topK, got %v", got)
	}
}

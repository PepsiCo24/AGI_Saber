"""文本切分器（与 main 分支 Go 版 internal/domain/rag/splitter.go 对齐）。

策略要点：
  1. 真递归分隔符栈：默认顺序 ["\\n\\n", "\\n", "。", "！", "？", "；", " ", ""]，
     从粗到细逐层切分；某一片段仍超过 chunk_size 时降级到下一层分隔符继续切，
     最末层 "" 表示按 rune 硬切兜底。
  2. Markdown 保护：
     - 围栏代码块（``` ... ``` 和 ~~~ ... ~~~）整段作为不可切的原子片段，
       从原文中临时挖出，递归切分只对非代码段执行，最终保留原代码块完整。
     - 标题行（行首 ^#{1,6} ）若单独成段，会与紧随其后的片段粘合，
       避免标题后立即断开。
  3. tail-rune overlap：相邻 chunk 之间，把上一个 chunk 末尾 chunk_overlap 个
     rune 作为前缀拼到下一个 chunk 开头（Python str 切片 s[-N:] 即按 rune 取，
     不会出现半字）；overlap=0 时不前缀。
"""

import re
from dataclasses import dataclass
from typing import List, Optional, Tuple


@dataclass
class Chunk:
    id: int
    content: str


_DEFAULT_SEPARATORS: List[str] = ["\n\n", "\n", "。", "！", "？", "；", " ", ""]

_FENCE_RE = re.compile(
    r"^(```|~~~)[^\n]*\n.*?^\1[ \t]*$\n?",
    re.MULTILINE | re.DOTALL,
)

_HEADING_RE = re.compile(r"^#{1,6} ")


class RecursiveSplitter:
    """递归分隔符栈 + Markdown 保护 + tail-rune overlap 的文本切分器。"""

    def __init__(self, chunk_size: int = 200, chunk_overlap: int = 50,
                 separators: Optional[List[str]] = None):
        self.chunk_size = max(1, int(chunk_size or 1))
        overlap = max(0, int(chunk_overlap or 0))
        if overlap >= self.chunk_size:
            overlap = self.chunk_size - 1
        self.chunk_overlap = overlap
        self.separators = list(separators) if separators else list(_DEFAULT_SEPARATORS)

    def split(self, text: str) -> List[Chunk]:
        if not text:
            return []

        atoms = self._protect_fences(text)

        pieces: List[Tuple[bool, str]] = []
        for is_atom, segment in atoms:
            if is_atom:
                pieces.append((True, segment))
                continue
            for p in self._recursive_split(segment, self.separators):
                pieces.append((False, p))

        merged = self._merge(pieces)
        merged = self._apply_overlap(merged)

        return [Chunk(id=i, content=c) for i, c in enumerate(merged)]

    def _protect_fences(self, text: str) -> List[Tuple[bool, str]]:
        atoms: List[Tuple[bool, str]] = []
        cursor = 0
        for m in _FENCE_RE.finditer(text):
            if m.start() > cursor:
                atoms.append((False, text[cursor:m.start()]))
            atoms.append((True, m.group(0)))
            cursor = m.end()
        if cursor < len(text):
            atoms.append((False, text[cursor:]))
        if not atoms:
            atoms = [(False, text)]
        return atoms

    def _recursive_split(self, text: str, seps: List[str]) -> List[str]:
        if len(text) <= self.chunk_size:
            return [text] if text.strip() != "" else []
        if not seps:
            return self._hard_split(text)
        sep = seps[0]
        rest = seps[1:]
        if sep == "":
            return self._hard_split(text)

        parts = self._split_keep_sep(text, sep)
        # 没切出来（文本里压根没这个分隔符）：直接降级
        if len(parts) <= 1 and parts and parts[0] == text:
            return self._recursive_split(text, rest)

        out: List[str] = []
        for p in parts:
            if not p:
                continue
            if len(p) <= self.chunk_size:
                if p.strip() != "":
                    out.append(p)
            else:
                out.extend(self._recursive_split(p, rest))
        return out

    @staticmethod
    def _split_keep_sep(text: str, sep: str) -> List[str]:
        if sep == "":
            return [text]
        parts = text.split(sep)
        if len(parts) <= 1:
            return parts
        out = [parts[0]]
        for p in parts[1:]:
            out.append(sep + p)
        return out

    def _hard_split(self, text: str) -> List[str]:
        out: List[str] = []
        size = self.chunk_size
        for i in range(0, len(text), size):
            piece = text[i:i + size]
            if piece:
                out.append(piece)
        return out

    def _merge(self, pieces: List[Tuple[bool, str]]) -> List[str]:
        merged: List[str] = []
        buf = ""
        buf_heading_only = False

        def is_heading_only(s: str) -> bool:
            stripped = s.strip()
            if not stripped or "\n" in stripped:
                return False
            return bool(_HEADING_RE.match(stripped))

        for is_atom, p in pieces:
            if not p:
                continue
            if buf == "":
                buf = p
                buf_heading_only = (not is_atom) and is_heading_only(p)
                continue

            if buf_heading_only:
                buf = buf + p
                buf_heading_only = (not is_atom) and is_heading_only(buf)
                continue

            if is_atom:
                if len(buf) + len(p) <= self.chunk_size:
                    buf = buf + p
                    buf_heading_only = False
                    continue
                merged.append(buf)
                buf = p
                buf_heading_only = False
                continue

            if len(buf) + len(p) <= self.chunk_size:
                buf = buf + p
                buf_heading_only = is_heading_only(buf)
                continue

            merged.append(buf)
            buf = p
            buf_heading_only = is_heading_only(p)

        if buf:
            merged.append(buf)
        return merged

    def _apply_overlap(self, merged: List[str]) -> List[str]:
        if self.chunk_overlap <= 0 or len(merged) <= 1:
            return merged
        out = [merged[0]]
        n = self.chunk_overlap
        for i in range(1, len(merged)):
            prev = merged[i - 1]
            tail = prev[-n:] if len(prev) >= n else prev
            out.append(tail + merged[i])
        return out

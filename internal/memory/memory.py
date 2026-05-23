# memory — 三层记忆系统（短期 / 长期 / 用户偏好）
import json
import logging
import math
from dataclasses import dataclass, field
from typing import List, Optional, Dict, Any

from config.config import APIConfig
from internal.infra.infra import Infrastructure

logger = logging.getLogger(__name__)


@dataclass
class Item:
    content: str
    importance: float = 0.5
    embedding: Optional[List[float]] = None


class ShortTerm:
    """短期记忆 - 滑动窗口存储最近 N 轮对话"""

    def __init__(self, max_turns: int = 10):
        self.max_turns = max_turns
        self.messages: List[Dict[str, str]] = []

    def add(self, role: str, content: str):
        """添加一条消息到短期记忆"""
        self.messages.append({"role": role, "content": content})
        # 保持滑动窗口
        while len(self.messages) > self.max_turns * 2:  # 每轮包含用户和助手两条消息
            self.messages.pop(0)

    def get(self) -> List[Dict[str, str]]:
        """获取所有短期记忆消息"""
        return self.messages.copy()

    def clear(self):
        """清空短期记忆"""
        self.messages = []

    def count(self) -> int:
        """返回消息数量"""
        return len(self.messages)


class LongTerm:
    """长期记忆 - 基于 Embedding 的语义向量存储"""

    def __init__(self, cfg: APIConfig, inf: Infrastructure):
        self.cfg = cfg
        self.inf = inf
        self.items: List[Item] = []
        self._embed_fn: Optional[Any] = None

    def set_embed_fn(self, fn):
        """设置 Embedding 函数"""
        self._embed_fn = fn

    def load_from_storage(self):
        """从 PostgreSQL 恢复长期记忆"""
        rows = self.inf.load_long_term_items()
        for row in rows:
            self.items.append(Item(
                content=row.content,
                importance=row.importance,
                embedding=row.embedding
            ))
        logger.info(f"✅ 从存储恢复了 {len(self.items)} 条长期记忆")

    def add(self, content: str, importance: float = 0.5):
        """添加一条长期记忆"""
        embedding = None
        if self._embed_fn:
            try:
                embedding = self._embed_fn(content)
            except Exception as e:
                logger.warning("⚠️  向量化失败: %s", e)

        item = Item(content=content, importance=importance, embedding=embedding)
        self.items.append(item)

        # 持久化到 PostgreSQL
        emb_json = json.dumps(embedding) if embedding else "null"
        self.inf.save_long_term_item(content, importance, emb_json)

    def recall(self, query: str, top_k: int = 3) -> List[Item]:
        """基于语义相似度召回相关记忆"""
        if not self.items:
            return []

        query_emb = None
        if self._embed_fn:
            try:
                query_emb = self._embed_fn(query)
            except Exception as e:
                logger.warning("⚠️  查询向量化失败: %s", e)
                return []

        if not query_emb:
            return self.items[:top_k]

        # 计算相似度并排序
        results = []
        for item in self.items:
            if item.embedding:
                sim = self._cosine_similarity(query_emb, item.embedding)
                # 综合相似度和重要性
                score = sim * 0.7 + item.importance * 0.3
                results.append((item, score))

        results.sort(key=lambda x: x[1], reverse=True)
        return [item for item, score in results[:top_k] if score >= 0.4]

    def _cosine_similarity(self, a: List[float], b: List[float]) -> float:
        """计算余弦相似度"""
        if len(a) != len(b):
            return 0.0
        dot = sum(x * y for x, y in zip(a, b))
        na = math.sqrt(sum(x * x for x in a))
        nb = math.sqrt(sum(x * x for x in b))
        if na == 0 or nb == 0:
            return 0.0
        return dot / (na * nb)

    def consolidate(self):
        """记忆合并 - 去重和重要性衰减"""
        if len(self.items) < self.cfg.memory_consolidation_trigger:
            return

        logger.info("🔄 开始记忆合并...")

        # 去重：相似度超过阈值的合并
        to_remove = set()
        for i in range(len(self.items)):
            if i in to_remove:
                continue
            for j in range(i + 1, len(self.items)):
                if j in to_remove:
                    continue
                sim = self._compute_similarity(self.items[i].content, self.items[j].content)
                if sim >= self.cfg.memory_consolidation_dedup:
                    # 合并到重要性更高的条目
                    if self.items[i].importance < self.items[j].importance:
                        to_remove.add(i)
                    else:
                        to_remove.add(j)

        # 移除重复项
        self.items = [item for i, item in enumerate(self.items) if i not in to_remove]

        # 重要性衰减
        for item in self.items:
            item.importance *= self.cfg.memory_consolidation_decay_rate

        # 过滤低重要性的记忆
        self.items = [item for item in self.items if item.importance >= self.cfg.memory_consolidation_min_import]

        logger.info(f"✅ 记忆合并完成，剩余 {len(self.items)} 条")

    def _compute_similarity(self, a: str, b: str) -> float:
        """简单的文本相似度计算"""
        tokens_a = set(a.lower().split())
        tokens_b = set(b.lower().split())
        if not tokens_a or not tokens_b:
            return 0.0
        return len(tokens_a & tokens_b) / len(tokens_a | tokens_b)


class Preference:
    """用户偏好管理"""

    def __init__(self, user_id: str, inf: Infrastructure):
        self.user_id = user_id
        self.inf = inf
        self.preferences: Dict[str, str] = {}
        self.load_from_storage()

    def load_from_storage(self):
        """从 PostgreSQL 加载用户偏好"""
        self.preferences = self.inf.load_preferences(self.user_id)
        logger.info(f"✅ 加载用户 {self.user_id} 的偏好: {self.preferences}")

    def set(self, key: str, value: str):
        """设置偏好"""
        self.preferences[key] = value
        self.inf.save_preference(self.user_id, key, value)

    def get(self, key: str, default: str = "") -> str:
        """获取偏好"""
        return self.preferences.get(key, default)

    def get_all(self) -> Dict[str, str]:
        """获取所有偏好"""
        return self.preferences.copy()

    def update_from_messages(self, messages: List[Dict[str, str]]):
        """从对话消息中更新偏好"""
        for msg in messages:
            content = msg.get("content", "")
            if "我叫" in content:
                import re
                match = re.search(r"我叫\s*(\S+)", content)
                if match:
                    self.set("name", match.group(1))
            if "喜欢" in content:
                match = re.search(r"喜欢\s*(\S+)", content)
                if match:
                    self.set("like", match.group(1))

# longterm — 长期记忆条目仓储（Postgres 实现）。
import json
import logging
import time
from dataclasses import dataclass, field
from typing import List, Optional

from internal.platform.postgres import PostgresClient

logger = logging.getLogger(__name__)


@dataclass
class Row:
    """长期记忆条目的领域模型。"""
    id: int = 0
    content: str = ""
    importance: float = 0.0
    embedding: List[float] = field(default_factory=list)
    created_at: float = 0.0
    last_accessed: float = 0.0
    category: str = ""
    tags: List[str] = field(default_factory=list)
    slot_hint: str = ""
    score: float = 0.0


def _emb_to_str(embedding_json) -> str:
    if isinstance(embedding_json, (bytes, bytearray)):
        try:
            return bytes(embedding_json).decode("utf-8")
        except Exception:
            return "[]"
    if embedding_json is None:
        return "null"
    return embedding_json


class PGRepo:
    """Postgres 实现；client 不可用时返回安全默认值。"""

    def __init__(self, client: PostgresClient):
        self.client = client

    # 默认写入：补齐时间戳，其余字段留默认（category/tags/slot_hint/score 由 store_classified 真填）
    def save(self, content: str, importance: float, embedding_json,
             created_at: Optional[float] = None,
             last_accessed: Optional[float] = None,
             category: str = "",
             tags: Optional[List[str]] = None,
             slot_hint: str = "",
             score: float = 0.0) -> int:
        if self.client is None or not self.client.is_real() or self.client.conn is None:
            return -1
        if created_at is None:
            created_at = time.time()
        if last_accessed is None:
            last_accessed = created_at
        if tags is None:
            tags = []
        emb_param = _emb_to_str(embedding_json)
        try:
            with self.client.conn.cursor() as cur:
                cur.execute(
                    "INSERT INTO long_term_memory "
                    "(content, importance, embedding, created_at, last_accessed, "
                    " category, tags, slot_hint, score) "
                    "VALUES (%s, %s, %s, %s, %s, %s, %s::jsonb, %s, %s) RETURNING id",
                    (content, importance, emb_param,
                     float(created_at), float(last_accessed),
                     category or "", json.dumps(list(tags)), slot_hint or "", float(score)),
                )
                row = cur.fetchone()
                return int(row[0]) if row else -1
        except Exception as e:
            logger.warning("⚠️  长期记忆保存失败: %s", e)
            return -1

    # 加载全部长期记忆条目
    def load(self) -> List[Row]:
        if self.client is None or not self.client.is_real():
            return []
        try:
            rows = self.client.query(
                "SELECT id, content, importance, embedding, "
                "created_at, last_accessed, "
                "COALESCE(category, ''), COALESCE(tags, '[]'::jsonb), "
                "COALESCE(slot_hint, ''), COALESCE(score, 0.0) "
                "FROM long_term_memory ORDER BY id"
            )
        except Exception as e:
            logger.warning("⚠️  加载长期记忆失败: %s", e)
            return []
        items: List[Row] = []
        for r in rows:
            try:
                rid, content, importance, emb_json, created_at, last_accessed, \
                    category, tags, slot_hint, score = (
                        r[0], r[1], r[2], r[3], r[4], r[5], r[6], r[7], r[8], r[9]
                    )
                embedding: List[float] = []
                if emb_json:
                    try:
                        if isinstance(emb_json, (bytes, bytearray)):
                            embedding = json.loads(bytes(emb_json).decode("utf-8"))
                        elif isinstance(emb_json, str):
                            embedding = json.loads(emb_json)
                        elif isinstance(emb_json, list):
                            embedding = emb_json
                    except Exception:
                        embedding = []
                # tags: psycopg2 在 JSONB 列上通常直接返回 list；兼容 str 形式
                if isinstance(tags, (bytes, bytearray)):
                    try:
                        tags = json.loads(bytes(tags).decode("utf-8"))
                    except Exception:
                        tags = []
                elif isinstance(tags, str):
                    try:
                        tags = json.loads(tags)
                    except Exception:
                        tags = []
                if not isinstance(tags, list):
                    tags = []

                def _to_ts(v):
                    if v is None:
                        return 0.0
                    if hasattr(v, "timestamp"):
                        try:
                            return float(v.timestamp())
                        except Exception:
                            return 0.0
                    try:
                        return float(v)
                    except Exception:
                        return 0.0

                items.append(Row(
                    id=int(rid),
                    content=content or "",
                    importance=float(importance) if importance is not None else 0.0,
                    embedding=embedding,
                    created_at=_to_ts(created_at),
                    last_accessed=_to_ts(last_accessed),
                    category=category or "",
                    tags=[str(t) for t in tags],
                    slot_hint=slot_hint or "",
                    score=float(score) if score is not None else 0.0,
                ))
            except Exception:
                continue
        return items

    # 修改一条长期记忆
    def update(self, item_id: int, content: str, importance: float, embedding_json) -> None:
        if self.client is None or not self.client.is_real():
            return
        emb_param = _emb_to_str(embedding_json)
        try:
            self.client.exec(
                "UPDATE long_term_memory SET content = %s, importance = %s, embedding = %s, "
                "last_accessed = EXTRACT(EPOCH FROM NOW()) WHERE id = %s",
                (content, importance, emb_param, item_id),
            )
        except Exception as e:
            logger.warning("⚠️  长期记忆更新失败 (id=%d): %s", item_id, e)

    # dedup 命中后只更新 Schema-driven 字段（不动 content/embedding）
    def update_classified(self, item_id: int, importance: float,
                          tags: List[str], category: str,
                          slot_hint: str, last_accessed: float) -> None:
        if self.client is None or not self.client.is_real():
            return
        try:
            self.client.exec(
                "UPDATE long_term_memory SET importance = %s, tags = %s::jsonb, "
                "category = %s, slot_hint = %s, last_accessed = %s WHERE id = %s",
                (float(importance), json.dumps(list(tags or [])),
                 category or "", slot_hint or "",
                 float(last_accessed), item_id),
            )
        except Exception as e:
            logger.warning("⚠️  长期记忆 update_classified 失败 (id=%d): %s", item_id, e)

    # 批量删除
    def delete(self, ids: List[int]) -> None:
        if self.client is None or not self.client.is_real() or not ids:
            return
        placeholders = ",".join(["%s"] * len(ids))
        query = f"DELETE FROM long_term_memory WHERE id IN ({placeholders})"
        try:
            self.client.exec(query, tuple(ids))
        except Exception as e:
            logger.warning("⚠️  长期记忆批量删除失败: %s", e)

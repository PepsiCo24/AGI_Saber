# chathistory — 聊天记录仓储（Postgres 实现）。
# 写入 chat_history (role, content, created_at)。
import logging
from dataclasses import dataclass, field
from typing import List

from internal.platform.postgres import PostgresClient

logger = logging.getLogger(__name__)


@dataclass
class Entry:
    """一条聊天记录的领域模型。"""
    role: str = ""
    content: str = ""
    created_at: str = ""  # 'HH:MM:SS' 形式（用于回显）


class PGRepo:
    """Postgres 实现；client 不可用时所有方法降级为无操作。"""

    def __init__(self, client: PostgresClient):
        self.client = client

    # 持久化一条聊天记录
    def save(self, role: str, content: str) -> None:
        if self.client is None or not self.client.is_real():
            return
        try:
            self.client.exec(
                "INSERT INTO chat_history (role, content) VALUES (%s, %s)",
                (role, content),
            )
        except Exception as e:
            logger.warning("⚠️  聊天记录保存到 PG 失败: %s", e)

    # 加载最近 N 条聊天记录（按时间正序返回）
    def load(self, limit: int) -> List[Entry]:
        if self.client is None or not self.client.is_real():
            return []
        try:
            rows = self.client.query(
                "SELECT role, content, TO_CHAR(created_at, 'HH24:MI:SS') "
                "FROM chat_history ORDER BY id DESC LIMIT %s",
                (limit,),
            )
        except Exception as e:
            logger.warning("⚠️  加载聊天记录失败: %s", e)
            return []
        result: List[Entry] = []
        for row in rows:
            try:
                role, content, created_at = row[0], row[1], row[2]
                result.append(Entry(role=role, content=content, created_at=created_at or ""))
            except Exception:
                continue
        # 反转为时间正序
        result.reverse()
        return result

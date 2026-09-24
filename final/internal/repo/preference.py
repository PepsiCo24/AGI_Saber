# preference — 用户偏好仓储（Postgres 实现）。
import logging
from typing import Dict

from internal.platform.postgres import PostgresClient

logger = logging.getLogger(__name__)


class PGRepo:
    """Postgres 实现；client 不可用时降级为空操作。"""

    def __init__(self, client: PostgresClient):
        self.client = client

    # 写入或更新一条偏好
    def save(self, user_id: str, key: str, value: str) -> None:
        if self.client is None or not self.client.is_real():
            return
        try:
            self.client.exec(
                "INSERT INTO user_preferences (user_id, key, value) VALUES (%s, %s, %s) "
                "ON CONFLICT (user_id, key) DO UPDATE SET value = %s, updated_at = NOW()",
                (user_id, key, value, value),
            )
        except Exception as e:
            logger.warning("⚠️  偏好保存到 PG 失败: %s", e)

    # 返回该用户的所有偏好键值
    def load(self, user_id: str) -> Dict[str, str]:
        result: Dict[str, str] = {}
        if self.client is None or not self.client.is_real():
            return result
        try:
            rows = self.client.query(
                "SELECT key, value FROM user_preferences WHERE user_id = %s",
                (user_id,),
            )
        except Exception as e:
            logger.warning("⚠️  加载偏好失败: %s", e)
            return result
        for row in rows:
            try:
                result[row[0]] = row[1]
            except Exception:
                continue
        return result

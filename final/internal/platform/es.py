# es — Elasticsearch 平台层薄封装：连接、健康检查、关键索引/检索操作。
# 失败时降级到 mock（self._es 为 None），不阻塞应用启动。
import json
import logging
from typing import Any, Dict, List, Optional, Tuple

from config.config import APIConfig

logger = logging.getLogger(__name__)

try:
    from elasticsearch import Elasticsearch
    _HAS_ES = True
except ImportError:
    Elasticsearch = None  # type: ignore
    _HAS_ES = False


class ESClient:
    """Elasticsearch 平台客户端：连接、ping、index/search/delete 关键操作。"""

    def __init__(self, cfg: APIConfig):
        self.cfg = cfg
        self._es = None
        self.status: str = "disconnected"
        self._connect()

    # ─── 连接 ───
    def _connect(self) -> None:
        if not _HAS_ES:
            logger.warning("⚠️  elasticsearch-py 未安装，ES 不可用")
            return
        if not self.cfg.es_addresses:
            logger.warning("⚠️  Elasticsearch 未配置")
            return
        try:
            auth: Optional[Tuple[str, str]] = None
            if self.cfg.es_username:
                auth = (self.cfg.es_username, self.cfg.es_password)
            self._es = Elasticsearch(self.cfg.es_addresses, basic_auth=auth)
            if not self._es.ping():
                logger.warning("⚠️  Elasticsearch Ping 失败")
                self._es = None
                return
            self.status = "connected"
            logger.info("✅ Elasticsearch 已连接: %s", self.cfg.es_addresses)
        except Exception as e:
            logger.warning("⚠️  Elasticsearch 连接失败: %s", e)
            self._es = None

    # ─── 状态判断 ───
    def is_real(self) -> bool:
        return self._es is not None

    @property
    def client(self):
        """暴露底层 Elasticsearch 客户端，供高级用法。"""
        return self._es

    # ─── 关键操作 ───
    def index(self, index: str, doc_id: Any, body: Dict[str, Any]) -> bool:
        """写入/更新文档，失败返回 False。"""
        if self._es is None:
            return False
        try:
            self._es.index(index=index, id=doc_id, body=body)
            return True
        except Exception as e:
            logger.warning("⚠️  ES 索引失败: %s", e)
            return False

    def search(self, index: str, body: Dict[str, Any]) -> Dict[str, Any]:
        """通用检索，返回原始响应（dict）；失败返回 {}。"""
        if self._es is None:
            return {}
        try:
            resp = self._es.search(index=index, body=body)
            # elasticsearch-py 8.x 返回 ObjectApiResponse；用 dict() 兼容
            return dict(resp)
        except Exception as e:
            logger.warning("⚠️  ES 检索失败: %s", e)
            return {}

    def search_raw(self, index: str, query_json: str) -> str:
        """以 JSON 字符串入参与返回，便于跨语言桥接。"""
        if self._es is None:
            raise RuntimeError("elasticsearch not connected")
        resp = self._es.search(index=index, body=json.loads(query_json))
        return json.dumps(dict(resp))

    def delete(self, index: str, doc_id: Any) -> bool:
        if self._es is None:
            return False
        try:
            self._es.delete(index=index, id=doc_id)
            return True
        except Exception as e:
            logger.warning("⚠️  ES 删除失败: %s", e)
            return False

    def delete_many(self, index: str, doc_ids: List[Any]) -> None:
        if self._es is None:
            return
        for doc_id in doc_ids:
            try:
                self._es.delete(index=index, id=doc_id)
            except Exception as e:
                logger.warning("⚠️  ES 删除失败 (id=%s): %s", doc_id, e)

    # ─── 关闭 ───
    def close(self) -> None:
        if self._es is not None:
            try:
                self._es.close()
            except Exception as e:
                logger.warning("⚠️  ES 关闭失败: %s", e)
            finally:
                self._es = None
                self.status = "disconnected"

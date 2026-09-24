# neo4j — Neo4j 图数据库平台层薄封装：连接、约束/索引初始化、cypher 执行。
# 失败时降级到不可用（self._driver 为 None），不阻塞应用启动。
import logging
from typing import Any, Dict, List, Optional

from config.config import APIConfig

logger = logging.getLogger(__name__)

try:
    from neo4j import GraphDatabase, basic_auth
    _HAS_NEO4J = True
except ImportError:
    GraphDatabase = None  # type: ignore
    basic_auth = None  # type: ignore
    _HAS_NEO4J = False


# 启动期幂等创建的约束/索引（与 Go 版一致）
_CONSTRAINTS: List[str] = [
    "CREATE CONSTRAINT entity_name IF NOT EXISTS FOR (e:Entity) REQUIRE e.name IS UNIQUE",
    "CREATE INDEX entity_type IF NOT EXISTS FOR (e:Entity) ON (e.type)",
    "CREATE INDEX memory_node_id IF NOT EXISTS FOR (m:Memory) ON (m.mem_id)",
]


class Neo4jClient:
    """Neo4j 平台客户端：连接、约束/索引初始化、cypher 执行。"""

    def __init__(self, cfg: APIConfig):
        self.cfg = cfg
        self._driver = None
        self.status: str = "disconnected"
        self._connect()
        if self._driver is not None:
            self.ensure_constraints()

    # ─── 连接 ───
    def _connect(self) -> None:
        if not _HAS_NEO4J:
            logger.warning("⚠️  neo4j Python driver 未安装，知识图谱将降级跳过")
            return
        if not self.cfg.kg_enabled or not self.cfg.neo4j_uri:
            logger.info(
                "ℹ️  Neo4j 未启用（kg_enabled=%s, uri=%r）",
                self.cfg.kg_enabled, self.cfg.neo4j_uri,
            )
            return
        try:
            self._driver = GraphDatabase.driver(
                self.cfg.neo4j_uri,
                auth=basic_auth(self.cfg.neo4j_user, self.cfg.neo4j_password),
            )
            # 连通性验证（driver 内部含超时设置）
            self._driver.verify_connectivity()
            self.status = "connected"
            logger.info("✅ Neo4j 已连接: %s", self.cfg.neo4j_uri)
        except Exception as e:
            logger.warning("⚠️  Neo4j 连接失败: %s（知识图谱将降级跳过）", e)
            try:
                if self._driver is not None:
                    self._driver.close()
            except Exception:
                pass
            self._driver = None

    # ─── 状态判断 ───
    def is_real(self) -> bool:
        return self._driver is not None

    def available(self) -> bool:
        """与 Go 版 Available() 同名；返回是否可用。"""
        return self._driver is not None

    @property
    def driver(self):
        return self._driver

    # ─── Session ───
    def session(self):
        """返回 driver 自带 session（默认写模式）；调用方负责 close 或 with-block。"""
        if self._driver is None:
            return None
        return self._driver.session(default_access_mode="WRITE")

    # ─── 约束 / 索引 ───
    def ensure_constraints(self) -> None:
        """启动期幂等执行约束 / 索引创建（已存在或版本不支持时忽略）。"""
        if self._driver is None:
            return
        try:
            with self._driver.session(default_access_mode="WRITE") as sess:
                for q in _CONSTRAINTS:
                    try:
                        sess.run(q)
                    except Exception as e:
                        logger.info("ℹ️  Neo4j constraint/index: %s", e)
        except Exception as e:
            logger.warning("⚠️  Neo4j ensure_constraints 失败: %s", e)

    # ─── Cypher 执行 ───
    def run_cypher(self, query: str, params: Optional[Dict[str, Any]] = None) -> List[Dict[str, Any]]:
        """执行 cypher 并返回结果（每行作为 dict）。失败返回空列表。"""
        if self._driver is None:
            return []
        try:
            with self._driver.session(default_access_mode="WRITE") as sess:
                result = sess.run(query, params or {})
                return [record.data() for record in result]
        except Exception as e:
            logger.warning("⚠️  Neo4j cypher 执行失败: %s", e)
            return []

    def execute_write(self, query: str, params: Optional[Dict[str, Any]] = None) -> bool:
        """便捷写操作；失败返回 False。"""
        if self._driver is None:
            return False
        try:
            with self._driver.session(default_access_mode="WRITE") as sess:
                sess.run(query, params or {})
            return True
        except Exception as e:
            logger.warning("⚠️  Neo4j 写操作失败: %s", e)
            return False

    # ─── 关闭 ───
    def close(self) -> None:
        if self._driver is not None:
            try:
                self._driver.close()
            except Exception as e:
                logger.warning("⚠️  Neo4j 关闭失败: %s", e)
            finally:
                self._driver = None
                self.status = "disconnected"

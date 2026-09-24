# kafka — Kafka producer 平台层薄封装：连接、produce、关闭。
# 失败时降级到 mock（self._producer 为 None），事件回退到日志输出。
import json
import logging
from typing import Any, Optional, Union

from config.config import APIConfig

logger = logging.getLogger(__name__)

try:
    from kafka import KafkaProducer
    _HAS_KAFKA = True
except ImportError:
    KafkaProducer = None  # type: ignore
    _HAS_KAFKA = False


class KafkaClient:
    """Kafka 平台客户端：连接、produce 关键操作。

    Go 版无 broker 时仍返回 writer + "disconnected"；Python 这里直接给 None，
    并由 produce 落入日志回退（与 Go 版的 fallback-to-log 行为一致）。
    """

    def __init__(self, cfg: APIConfig):
        self.cfg = cfg
        self._producer = None
        self.status: str = "disconnected"
        self._connect()

    # ─── 连接 ───
    def _connect(self) -> None:
        if not _HAS_KAFKA:
            logger.warning("⚠️  kafka-python 未安装，Kafka 不可用 (事件将输出到日志)")
            return
        if not self.cfg.kafka_brokers:
            logger.warning("⚠️  Kafka 未配置 broker (事件将输出到日志)")
            return
        try:
            # value 用 raw bytes 写入；key 也按 bytes；上层负责 encode/序列化。
            self._producer = KafkaProducer(
                bootstrap_servers=self.cfg.kafka_brokers,
                # 与 Go segmentio LeastBytes 类似，kafka-python 默认按 partition key 路由；
                # 这里统一以 bytes 序列化，业务侧自由决定 payload 结构
                value_serializer=lambda v: v if isinstance(v, (bytes, bytearray)) else json.dumps(v).encode("utf-8"),
                # batch 与 Go 端 10ms 量级对齐
                linger_ms=10,
            )
            self.status = "connected"
            logger.info("✅ Kafka 已连接: %s", self.cfg.kafka_brokers)
        except Exception as e:
            logger.warning("⚠️  Kafka 连接失败: %s (事件将输出到日志)", e)
            self._producer = None

    # ─── 状态判断 ───
    def is_real(self) -> bool:
        return self._producer is not None

    @property
    def producer(self):
        return self._producer

    # ─── 关键操作 ───
    def produce(self, event_type: str, payload: Union[str, bytes, dict],
                topic: Optional[str] = None) -> bool:
        """发布事件；失败或未连接时回退到日志输出。"""
        target_topic = topic or self.cfg.kafka_topic
        if self._producer is None or self.status != "connected":
            logger.info("📋 [Kafka-fallback] %s: %s", event_type, payload)
            return False
        try:
            value: Any
            if isinstance(payload, (bytes, bytearray)):
                value = bytes(payload)
            elif isinstance(payload, str):
                value = payload.encode("utf-8")
            else:
                value = payload
            self._producer.send(
                target_topic,
                key=event_type.encode("utf-8"),
                value=value,
            )
            return True
        except Exception as e:
            logger.warning("⚠️  Kafka 写入失败: %s", e)
            logger.info("📋 [Kafka-fallback] %s: %s", event_type, payload)
            return False

    def flush(self) -> None:
        if self._producer is not None:
            try:
                self._producer.flush()
            except Exception as e:
                logger.warning("⚠️  Kafka flush 失败: %s", e)

    # ─── 关闭 ───
    def close(self) -> None:
        if self._producer is not None:
            try:
                self._producer.flush()
                self._producer.close()
            except Exception as e:
                logger.warning("⚠️  Kafka 关闭失败: %s", e)
            finally:
                self._producer = None
                self.status = "disconnected"

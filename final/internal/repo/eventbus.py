# eventbus — 事件发布的薄抽象，连接 Kafka 时写消息，否则降级为日志。
import logging
from typing import Optional

from internal.platform.kafka import KafkaClient

logger = logging.getLogger(__name__)


class KafkaPublisher:
    """Kafka 实现；client 不可用时所有 publish 退化为日志。"""

    def __init__(self, client: KafkaClient, available: Optional[bool] = None):
        self.client = client
        # available=None 时根据底层 client 判断；显式传 False 时强制降级为日志
        if available is None:
            self.available = bool(client is not None and client.is_real())
        else:
            self.available = available

    def publish(self, event_type: str, payload: str) -> None:
        """发布事件，连接不可用时输出到日志。"""
        if self.available and self.client is not None and self.client.is_real():
            try:
                self.client.produce(event_type, payload)
                return
            except Exception as e:
                logger.warning("⚠️  Kafka 写入失败: %s", e)
                # 落入日志回退
        logger.info("📋 [Kafka-fallback] %s: %s", event_type, payload)

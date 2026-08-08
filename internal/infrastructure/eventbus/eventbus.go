// Package eventbus 是事件发布的薄抽象，连接 Kafka 时写消息，否则降级为日志。
package eventbus

import (
	"context"

	"agi-assistant/internal/pkg/logger"

	"github.com/segmentio/kafka-go"
)

// Publisher 事件发布者接口
type Publisher interface {
	Publish(eventType, payload string)
}

// KafkaPublisher 是 Kafka 实现
type KafkaPublisher struct {
	w         *kafka.Writer
	available bool
}

// NewKafkaPublisher 创建 Kafka 实现；available=false 时所有 Publish 退化为日志
func NewKafkaPublisher(w *kafka.Writer, available bool) *KafkaPublisher {
	return &KafkaPublisher{w: w, available: available}
}

// Publish 发布事件，连接不可用时输出到日志
func (p *KafkaPublisher) Publish(eventType, payload string) {
	if p.available && p.w != nil {
		msg := kafka.Message{
			Key:   []byte(eventType),
			Value: []byte(payload),
		}
		if err := p.w.WriteMessages(context.Background(), msg); err != nil {
			logger.L().Warn("kafka write failed", "event_type", eventType, "err", err)
		}
		return
	}
	logger.L().Info("kafka fallback (log-only)", "event_type", eventType, "payload", payload)
}

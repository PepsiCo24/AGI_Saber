// Package kafka 提供 Kafka Writer 的薄封装。
package kafka

import (
	"context"
	"time"

	"agi-assistant/config"
	"agi-assistant/internal/pkg/logger"

	kafkago "github.com/segmentio/kafka-go"
)

// Connect 创建 Writer 并尝试 DialLeader 验证 broker 可达。
// 任何阶段失败都返回 (writer, "disconnected") —— writer 仍可用于发布失败回退到日志的场景。
func Connect(cfg config.KafkaConfig) (*kafkago.Writer, string) {
	w := &kafkago.Writer{
		Addr:         kafkago.TCP(cfg.KafkaBrokers...),
		Topic:        cfg.KafkaTopic,
		Balancer:     &kafkago.LeastBytes{},
		BatchTimeout: 10 * time.Millisecond,
	}
	if len(cfg.KafkaBrokers) == 0 {
		logger.L().Warn("kafka broker not configured, events will fall back to log")
		return w, "disconnected"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := kafkago.DialLeader(ctx, "tcp", cfg.KafkaBrokers[0], cfg.KafkaTopic, 0)
	if err != nil {
		logger.L().Warn("kafka connect failed, events will fall back to log", "err", err)
		return w, "disconnected"
	}
	conn.Close()
	logger.L().Info("kafka connected", "brokers", cfg.KafkaBrokers)
	return w, "connected"
}

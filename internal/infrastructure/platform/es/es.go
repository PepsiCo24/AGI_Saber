// Package es 提供 Elasticsearch 连接的薄封装。
package es

import (
	"agi-assistant/config"
	"agi-assistant/internal/pkg/logger"

	es "github.com/elastic/go-elasticsearch/v8"
)

// Connect 尝试连接 ES 并 Info()-ping 验证。
// 失败时返回 (nil, "disconnected")。
func Connect(cfg config.ESConfig) (*es.Client, string) {
	esCfg := es.Config{
		Addresses: cfg.ESAddresses,
		Username:  cfg.ESUsername,
		Password:  cfg.ESPassword,
	}
	client, err := es.NewClient(esCfg)
	if err != nil {
		logger.L().Warn("elasticsearch connect failed", "err", err)
		return nil, "disconnected"
	}
	res, err := client.Info()
	if err != nil {
		logger.L().Warn("elasticsearch ping failed", "err", err)
		return nil, "disconnected"
	}
	res.Body.Close()
	logger.L().Info("elasticsearch connected", "addresses", cfg.ESAddresses)
	return client, "connected"
}

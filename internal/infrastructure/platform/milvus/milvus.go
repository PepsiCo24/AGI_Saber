// Package milvus 提供 Milvus 向量数据库连接的薄封装。
// 业务级集合管理与搜索方法不放这里——保持平台层无业务语义。
package milvus

import (
	"context"
	"time"

	"agi-assistant/config"
	"agi-assistant/internal/pkg/logger"

	milvusClient "github.com/milvus-io/milvus-sdk-go/v2/client"
)

// Connect 尝试连接 Milvus；失败时返回 (nil, "disconnected")，调用方决定是否降级。
func Connect(cfg config.MilvusConfig) (milvusClient.Client, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mc, err := milvusClient.NewClient(ctx, milvusClient.Config{
		Address: cfg.MilvusAddr(),
	})
	if err != nil {
		logger.L().Warn("milvus connect failed, in-memory fallback", "err", err)
		return nil, "disconnected"
	}
	logger.L().Info("milvus connected", "addr", cfg.MilvusAddr())
	return mc, "connected"
}

// Package neo4j 提供 Neo4j 驱动连接的薄封装与启动期约束/索引初始化。
// 业务级图操作（实体/关系 / 记忆图节点边）由 graph 与 memory domain 层负责。
package neo4j

import (
	"context"
	"time"

	"agi-assistant/config"
	"agi-assistant/internal/pkg/logger"

	driver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Client 持有 Neo4j 驱动连接
type Client struct {
	driver    driver.DriverWithContext
	available bool
}

// Connect 创建连接；连接失败时返回不可用实例，不阻塞启动。
// 由调用方在启动后调用 EnsureConstraints 应用基础约束。
func Connect(cfg config.Neo4jConfig) *Client {
	if !cfg.KGEnabled || cfg.Neo4jURI == "" {
		logger.L().Info("neo4j disabled",
			"kg_enabled", cfg.KGEnabled, "uri", cfg.Neo4jURI)
		return &Client{available: false}
	}

	d, err := driver.NewDriverWithContext(
		cfg.Neo4jURI,
		driver.BasicAuth(cfg.Neo4jUser, cfg.Neo4jPassword, ""),
	)
	if err != nil {
		logger.L().Warn("neo4j driver init failed, KG will be skipped", "err", err)
		return &Client{available: false}
	}

	// 连通性验证（超时 5s）
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.VerifyConnectivity(ctx); err != nil {
		logger.L().Warn("neo4j connectivity check failed, KG will be skipped", "err", err)
		_ = d.Close(context.Background())
		return &Client{available: false}
	}

	c := &Client{driver: d, available: true}
	c.EnsureConstraints()
	logger.L().Info("neo4j connected", "uri", cfg.Neo4jURI)
	return c
}

// Available 报告 Neo4j 是否可用
func (c *Client) Available() bool { return c.available }

// Close 关闭驱动
func (c *Client) Close() {
	if c.driver != nil {
		c.driver.Close(context.Background())
	}
}

// Session 返回一个写入 session；调用方需 defer session.Close
func (c *Client) Session() driver.SessionWithContext {
	return c.driver.NewSession(context.Background(), driver.SessionConfig{
		AccessMode: driver.AccessModeWrite,
	})
}

// EnsureConstraints 确保 Neo4j 中存在唯一约束/索引（幂等）
func (c *Client) EnsureConstraints() {
	if !c.available {
		return
	}
	ctx := context.Background()
	sess := c.Session()
	defer sess.Close(ctx)

	queries := []string{
		`CREATE CONSTRAINT entity_name IF NOT EXISTS FOR (e:Entity) REQUIRE e.name IS UNIQUE`,
		`CREATE INDEX entity_type IF NOT EXISTS FOR (e:Entity) ON (e.type)`,
		`CREATE CONSTRAINT memory_node_id IF NOT EXISTS FOR (m:Memory) REQUIRE m.memory_id IS UNIQUE`,
	}
	for _, q := range queries {
		if _, err := sess.Run(ctx, q, nil); err != nil {
			// 约束已存在或版本不支持时忽略
			logger.L().Info("neo4j constraint/index note", "err", err)
		}
	}
}

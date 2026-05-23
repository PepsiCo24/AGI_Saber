// Final Stage — 全阶段整合 AI 助手
//
// 目录结构：
//
//	config/           配置（环境变量读取 + 默认值）
//	internal/
//	  infra/          基础设施连接（Milvus / PostgreSQL / ES / Kafka）
//	  llm/            LLM 客户端（真实 API + Mock 降级）
//	  rag/            RAG 引擎（文本切分 + TF 向量检索）
//	  tools/          工具定义与调用（time / weather / search）
//	  memory/         三层记忆（短期 / 长期 / 用户偏好）
//	  agent/          UnifiedAgent（ReAct + Harness + 智能路由）
//	  handler/        HTTP API 路由处理
//	frontend/         单文件前端 HTML
package main

import (
	"final/config"
	"final/internal/agent"
	"final/internal/handler"
	"final/internal/infra"
	"fmt"
	"log"
	"net/http"
)

func main() {
	cfg := config.DefaultConfig()

	// 初始化基础设施（失败则降级，不阻塞启动）
	log.Println("🔧 正在连接基础设施...")
	inf := infra.New(cfg)
	defer inf.Close()

	// 初始化 UnifiedAgent
	a := agent.New(cfg, inf)

	// 注册 HTTP 路由
	handler.New(a, inf, cfg)

	// 挂载前端静态资源
	http.Handle("/", http.FileServer(http.Dir("frontend")))

	printBanner(cfg, inf)

	addr := ":" + cfg.ServerPort
	log.Fatal(http.ListenAndServe(addr, nil))
}

func printBanner(cfg *config.APIConfig, inf *infra.Infrastructure) {
	addr := ":" + cfg.ServerPort
	fmt.Println("========================================")
	fmt.Println("Final Stage · AGI 智能助手启动成功")
	fmt.Println("========================================")

	fmt.Printf("[INFO] Service       http://localhost%s\n", addr)
	fmt.Printf("[INFO] 通用模型           %s\n", cfg.LLMModel)
	fmt.Printf("[INFO] Embedding     %s\n", cfg.EmbeddingModel)

	fmt.Println("----------------------------------------")

	fmt.Printf("[INFO] Milvus        %s\n", inf.Ready.Milvus)
	fmt.Printf("[INFO] PostgreSQL    %s:%d\n", cfg.PGHost, cfg.PGPort)
	fmt.Printf("[INFO] ElasticSearch %s\n", inf.Ready.ES)
	fmt.Printf("[INFO] Kafka         %s\n", inf.Ready.Kafka)

	fmt.Println("----------------------------------------")
	fmt.Println("[READY] 道阻且长，行则将至。")
	fmt.Println("========================================")
}

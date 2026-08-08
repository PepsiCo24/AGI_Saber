// Final Stage — 全阶段整合 AI 助手
//
// 目录结构：
//
//	config/                配置（YAML 加载 + 默认值，分组子结构）
//	internal/
//	  promptctx/           Schema-driven prompt context 装配
//	  llm/                 LLM 客户端（真实 API + Mock 降级）
//	  rag/                 RAG 引擎（文本切分 + 三路混合检索）
//	  tools/               工具定义与调用（time / weather / search / exec_command / MCP）
//	  memory/              三层记忆（短期 / 长期 / 用户偏好 / 图记忆）
//	  graph/                知识图谱业务（实体抽取 + 文档图）
//	  sandbox/             命令沙箱执行
//	  agent/               UnifiedAgent（ReAct + Harness + 智能路由）
//	  handler/             HTTP API（chi.Router + 中间件）
//	  middleware/          requestID / panicRecover / accessLog / cors
//	  platform/            平台层连接封装（milvus / postgres / es / kafka / neo4j）
//	  repo/                数据访问仓储
//	frontend/              单文件前端 HTML
//
// 启动 → 退出全流程：
//
//  1. 加载配置 + 连接基础设施（每路独立失败降级）
//  2. 构建 UnifiedAgent + HTTP Server（chi 路由 + 中间件 + 三大 timeout）
//  3. 监听 SIGINT / SIGTERM
//  4. 收到信号后 srv.Shutdown(30s) 等 in-flight 请求完成
//  5. 关闭外部连接（顺序：HTTP → DB → 队列）
package main

import (
	"agi-assistant/config"
	authapp "agi-assistant/internal/application/auth"
	"agi-assistant/internal/application/chat"
	"agi-assistant/internal/application/memoryprojection"
	"agi-assistant/internal/application/memoryreconcile"
	skillapp "agi-assistant/internal/application/skill"
	authdomain "agi-assistant/internal/domain/auth"
	"agi-assistant/internal/infrastructure/eventbus"
	"agi-assistant/internal/infrastructure/llm"
	"agi-assistant/internal/infrastructure/persistence/chathistory"
	"agi-assistant/internal/infrastructure/persistence/documentrepo"
	"agi-assistant/internal/infrastructure/persistence/longterm"
	"agi-assistant/internal/infrastructure/persistence/memoryoutbox"
	"agi-assistant/internal/infrastructure/persistence/memorytx"
	"agi-assistant/internal/infrastructure/persistence/preference"
	"agi-assistant/internal/infrastructure/persistence/ragchunk"
	"agi-assistant/internal/infrastructure/persistence/skillrepo"
	"agi-assistant/internal/infrastructure/persistence/snapshot"
	"agi-assistant/internal/infrastructure/persistence/userrepo"
	"agi-assistant/internal/infrastructure/platform/es"
	"agi-assistant/internal/infrastructure/platform/kafka"
	"agi-assistant/internal/infrastructure/platform/milvus"
	"agi-assistant/internal/infrastructure/platform/postgres"
	"agi-assistant/internal/infrastructure/projection/milvusmemory"
	"agi-assistant/internal/infrastructure/projection/neo4jmemory"
	"agi-assistant/internal/infrastructure/skillhub"
	"agi-assistant/internal/interfaces/http/handler"
	"agi-assistant/internal/pkg/logger"
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// HTTP server 各档超时——所有 timeout 都是"不响应即断"，单位都给宽一点：
//   - ReadHeaderTimeout：客户端发送 header 的总时间。挡 Slowloris 攻击。
//   - ReadTimeout：含 body，POST 大对象（文档上传）也要在这内完成读取。
//   - WriteTimeout：从 header 读完到 response 写完。SSE 路由可能持续数分钟，
//     给 5 分钟兜底；超长流式建议改用 WebSocket 或在 handler 里 SetWriteDeadline(zero)。
//   - IdleTimeout：keep-alive 空闲连接最长保持时间，到点回收 goroutine。
const (
	httpReadHeaderTimeout = 10 * time.Second
	httpReadTimeout       = 60 * time.Second
	httpWriteTimeout      = 5 * time.Minute
	httpIdleTimeout       = 120 * time.Second
	shutdownTimeout       = 30 * time.Second
)

func main() {
	cfg := config.DefaultConfig()

	// 结构化日志：启动期用 text 格式便于本地看，生产改 json 需要读环境变量。
	// 后续 Phase 会把 format/level 挂到 config.yaml，这里先用工程常用默认。
	logger.Init(logger.Config{Format: "text", Level: "info", AddSource: false})

	// ── 平台层连接（每路独立失败降级，不阻塞启动）──
	logger.L().Info("bootstrap: connecting infrastructure")
	milvusClient, milvusStatus := milvus.Connect(cfg.MilvusConfig)
	pgDB, pgStatus := postgres.Connect(cfg.PostgresConfig)
	if pgDB != nil {
		postgres.BootstrapSchema(pgDB)
	}
	esClient, esStatus := es.Connect(cfg.ESConfig)
	kafkaWriter, kafkaStatus := kafka.Connect(cfg.KafkaConfig)

	// ── Skill 广场服务（供 agent 主循环拉取已开启 skill + HTTP handler 使用）──
	var skillHub *skillhub.Client
	if cfg.SkillHubEnabled {
		skillHub = skillhub.NewClient(
			cfg.SkillHubGitHubToken, cfg.SkillHubKeyword,
			time.Duration(cfg.SkillHubCacheTTLMin)*time.Minute,
		)
	}
	skillSvc := skillapp.NewService(skillrepo.NewPGRepo(pgDB), skillHub, llm.New(cfg), cfg.SkillHubEnabled)

	// ── 仓储层（接口实现）──
	memoryRepo := memorytx.New(pgDB)
	outboxRepo := memoryoutbox.New(pgDB)
	deps := chat.Deps{
		ChatRepo:     chathistory.NewPGRepo(pgDB),
		PrefRepo:     preference.NewPGRepo(pgDB),
		SnapRepo:     snapshot.NewPGRepo(pgDB),
		LTMRepo:      longterm.NewPGRepo(pgDB),
		MemoryTxRepo: memoryRepo,
		RAGChunkRepo: ragchunk.NewStore(pgDB, milvusClient, esClient),
		DocumentRepo: documentrepo.NewStore(pgDB, ".data/documents"),
		Events:       eventbus.NewKafkaPublisher(kafkaWriter, kafkaStatus == "connected"),
		InfraStatus: map[string]string{
			"milvus":        milvusStatus,
			"pg":            pgStatus,
			"elasticsearch": esStatus,
			"kafka":         kafkaStatus,
		},
		EnabledSkillTools: skillSvc.EnabledTools,
	}

	// ── 初始化 UnifiedAgent + Auth Service + HTTP Server ──
	a := chat.New(cfg, deps)

	// 补充「agent 内部初始化」的中间件状态到 banner / status：
	// Neo4j 知识图谱、命令沙箱、Skill 广场都是在 agent 内部装配的，
	// 平台层 4 件套之外，这里回填便于启动横幅与 /api/status 一并展示。
	if kg := a.KG(); kg != nil && kg.Available() {
		deps.InfraStatus["neo4j"] = "connected"
	} else {
		deps.InfraStatus["neo4j"] = "disconnected"
	}
	if sb := a.Sandbox(); sb != nil {
		deps.InfraStatus["sandbox"] = sb.Backend()
	} else {
		deps.InfraStatus["sandbox"] = "disabled"
	}
	if cfg.SkillHubEnabled {
		deps.InfraStatus["skillhub"] = "enabled"
	} else {
		deps.InfraStatus["skillhub"] = "disabled"
	}

	// pprof 是"暴露内部信息"的端点，配置错误会直接泄漏 goroutine 栈 / heap。
	// 开启但没配 admin token 视为 misconfig，启动期直接失败——不给"以为安全其实不安全"的机会。
	if cfg.PprofEnabled && cfg.PprofAdminToken == "" {
		log.Fatalf("❌ pprof 已启用但 admin_token 未配置，拒绝启动（请在 config.observability.pprof.admin_token 或 PPROF_ADMIN_TOKEN 环境变量注入随机 token）")
	}

	// JWT 签发器：secret 必须 ≥32 字节，缺失/过短直接启动失败（防 misconfig 上线）。
	ttl := time.Duration(cfg.JWTTTLHours) * time.Hour
	issuer, err := authdomain.NewTokenIssuer(cfg.JWTSecret, ttl, cfg.JWTIssuer)
	if err != nil {
		log.Fatalf("❌ JWT 签发器构造失败: %v（请通过环境变量 JWT_SECRET 注入 ≥32 字节随机字符串）", err)
	}
	authSvc := authapp.NewService(userrepo.NewPGRepo(pgDB), issuer)

	srv := newHTTPServer(cfg, a, authSvc, issuer, skillSvc)

	// ── 监听信号 + 启动 server ──
	// signal.NotifyContext 会在收到 SIGINT/SIGTERM 时 cancel ctx，
	// 主流程检测到 ctx.Done() 后开始优雅关停。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	memoryCtx, stopMemory := context.WithCancel(context.Background())
	var memoryWG sync.WaitGroup
	runWorker := func(worker *memoryprojection.Worker) {
		memoryWG.Add(1)
		go func() {
			defer memoryWG.Done()
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-memoryCtx.Done():
					return
				case <-ticker.C:
					if err := worker.RunOnce(memoryCtx); err != nil {
						logger.L().Warn("memory projection worker cycle failed", "err", err)
					}
				}
			}
		}()
	}
	if milvusClient != nil {
		store := milvusmemory.NewMilvusStore(milvusClient, cfg.RAGMilvusDim)
		if err := store.Init(memoryCtx); err != nil {
			logger.L().Warn("memory milvus projection disabled", "err", err)
		} else {
			runWorker(memoryprojection.NewWorker(outboxRepo, milvusmemory.New(store),
				memoryprojection.Config{WorkerID: "milvus-memory", BatchSize: 100, Lease: 30 * time.Second, MaxAttempts: 10}))
			reconciler := memoryreconcile.New(memoryRepo, store, outboxRepo, "milvus", 500)
			memoryWG.Add(1)
			go runReconciler(memoryCtx, &memoryWG, reconciler, 6*time.Hour)
		}
	}
	if kg := a.KG(); kg != nil && kg.Available() {
		store := neo4jmemory.NewNeo4jStore(kg.Client())
		runWorker(memoryprojection.NewWorker(outboxRepo, neo4jmemory.New(store),
			memoryprojection.Config{WorkerID: "neo4j-memory", BatchSize: 100, Lease: 30 * time.Second, MaxAttempts: 10}))
		reconciler := memoryreconcile.New(memoryRepo, store, outboxRepo, "neo4j", 500)
		memoryWG.Add(1)
		go runReconciler(memoryCtx, &memoryWG, reconciler, 6*time.Hour)
	}

	// 服务器在独立 goroutine 跑，主 goroutine 等信号
	serverErr := make(chan error, 1)
	go func() {
		printBanner(cfg, deps.InfraStatus)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	// 等以下三种事件之一触发关停：
	//   1) 系统信号（ctx.Done）→ 正常关停
	//   2) ListenAndServe 异常返回（端口占用等）→ 直接退出
	select {
	case err := <-serverErr:
		if err != nil {
			log.Fatalf("❌ HTTP 服务启动失败: %v", err)
		}
	case <-ctx.Done():
		logger.L().Info("shutdown signal received, starting graceful shutdown")
	}

	// ── Graceful Shutdown ──
	// 先关 HTTP server：拒绝新连接，等 in-flight 请求在 shutdownTimeout 内完成。
	// 再关基础设施 client：保证关闭时已无 handler 在用它们，避免半途断连产生坏数据。
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		logger.L().Warn("http shutdown returned error (in-flight requests may have timed out)", slog.Any("err", err))
	} else {
		logger.L().Info("http server closed")
	}
	stopMemory()
	memoryWG.Wait()

	// 关闭外部连接
	if milvusClient != nil {
		_ = milvusClient.Close()
	}
	if pgDB != nil {
		_ = pgDB.Close()
	}
	if kafkaWriter != nil {
		_ = kafkaWriter.Close()
	}
	logger.L().Info("all external connections released, process exiting")
}

func runReconciler(ctx context.Context, wg *sync.WaitGroup, reconciler *memoryreconcile.Reconciler, interval time.Duration) {
	defer wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if report, err := reconciler.RunOnce(ctx); err != nil {
				logger.L().Warn("memory reconciliation failed", "err", err)
			} else if report.RepairEnqueued > 0 {
				logger.L().Info("memory reconciliation enqueued repairs", "count", report.RepairEnqueued)
			}
		}
	}
}

// newHTTPServer 装配 chi.Router + 三大 timeout。
// 单独抽出便于测试（直接 srv.Handler.ServeHTTP）和未来加 TLS。
func newHTTPServer(cfg *config.APIConfig, a *chat.UnifiedAgent, authSvc *authapp.Service, issuer *authdomain.TokenIssuer, skills *skillapp.Service) *http.Server {
	h := handler.New(a, authSvc, issuer, cfg, skills)
	return &http.Server{
		Addr:              ":" + cfg.ServerPort,
		Handler:           h.Handler(),
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       httpReadTimeout,
		WriteTimeout:      httpWriteTimeout,
		IdleTimeout:       httpIdleTimeout,
	}
}

func printBanner(cfg *config.APIConfig, status map[string]string) {
	addr := ":" + cfg.ServerPort
	fmt.Println("========================================")
	fmt.Println("Final Stage · AGI 智能助手启动成功")
	fmt.Println("========================================")

	fmt.Printf("[INFO] Service       http://localhost%s\n", addr)
	fmt.Printf("[INFO] 通用模型           %s\n", cfg.LLMModel)
	fmt.Printf("[INFO] Embedding     %s\n", cfg.EmbeddingModel)

	fmt.Println("----------------------------------------")

	fmt.Printf("[INFO] Milvus        %s\n", status["milvus"])
	fmt.Printf("[INFO] PostgreSQL    %s:%d (%s)\n", cfg.PGHost, cfg.PGPort, status["pg"])
	fmt.Printf("[INFO] ElasticSearch %s\n", status["elasticsearch"])
	fmt.Printf("[INFO] Kafka         %s\n", status["kafka"])
	fmt.Printf("[INFO] Neo4j(KG)     %s\n", status["neo4j"])
	fmt.Printf("[INFO] Sandbox       %s\n", status["sandbox"])
	skillhub := status["skillhub"]
	if skillhub == "enabled" {
		skillhub = fmt.Sprintf("enabled (keyword: %s)", cfg.SkillHubKeyword)
	}
	fmt.Printf("[INFO] Skill广场      %s\n", skillhub)

	fmt.Println("----------------------------------------")
	fmt.Println("[READY] 道阻且长，行则将至。")
	fmt.Println("========================================")
}

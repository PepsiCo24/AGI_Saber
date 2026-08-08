package config

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"strings"

	"agi-assistant/internal/pkg/logger"

	"gopkg.in/yaml.v3"
)

// 子结构按职责分组。所有子结构以 embedded 方式放进 APIConfig，
// 让 cfg.LLMAPIUrl / cfg.PGHost 等老访问路径通过 Go 字段提升继续工作。

// ServerConfig 服务端口与运行参数
type ServerConfig struct {
	ServerPort string
}

// LLMConfig 聊天模型 API
type LLMConfig struct {
	LLMAPIUrl string
	LLMAPIKey string
	LLMModel  string
	// LLMFastModel 快模型：用于规划/参数抽取/子 Agent/RAG 改写精排等内部步骤。
	// 为空时回落为 LLMModel（无行为变化）。最终答案仍用 LLMModel。
	LLMFastModel string
	Temperature  float64
}

func (c LLMConfig) IsRealLLM() bool { return c.LLMAPIKey != "" }

// EmbeddingConfig 向量化模型 API
type EmbeddingConfig struct {
	EmbeddingAPIUrl string
	EmbeddingAPIKey string
	EmbeddingModel  string
}

func (c EmbeddingConfig) IsRealEmbedding() bool { return c.EmbeddingAPIKey != "" }

// MilvusConfig 向量数据库连接
type MilvusConfig struct {
	MilvusHost string
	MilvusPort int
}

func (c MilvusConfig) MilvusAddr() string {
	return fmt.Sprintf("%s:%d", c.MilvusHost, c.MilvusPort)
}

// PostgresConfig 关系型数据库连接
type PostgresConfig struct {
	PGHost     string
	PGPort     int
	PGUser     string
	PGPassword string
	PGDatabase string
}

func (c PostgresConfig) PGDSN() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		c.PGUser, c.PGPassword, c.PGHost, c.PGPort, c.PGDatabase)
}

// ESConfig Elasticsearch 连接
type ESConfig struct {
	ESAddresses []string
	ESUsername  string
	ESPassword  string
}

// KafkaConfig 事件总线
type KafkaConfig struct {
	KafkaBrokers []string
	KafkaTopic   string
}

// Neo4jConfig 知识图谱后端
type Neo4jConfig struct {
	Neo4jURI      string
	Neo4jUser     string
	Neo4jPassword string
	KGMaxHops     int     // 图遍历最大跳数
	KGWeight      float64 // 图检索在 RRF 中的权重
	KGEnabled     bool    // 是否启用知识图谱
}

// StorageConfig 聚合所有外部存储/连接配置
type StorageConfig struct {
	MilvusConfig
	PostgresConfig
	ESConfig
	KafkaConfig
	Neo4jConfig
}

// RAGConfig 检索增强生成
type RAGConfig struct {
	ChunkSize          int
	ChunkOverlap       int
	TopK               int
	RRFConstantK       int
	SemanticWeight     float64
	EnableHybridSearch bool
	RAGMilvusDim       int

	// Query Rewrite（history-aware + multi-query）
	RAGRewriteEnabled    bool
	RAGRewriteNumQueries int // 含原查询在内的目标改写条数

	// Rerank（LLM listwise 精排）
	RAGRerankEnabled    bool
	RAGRerankPreviewLen int // 给 reranker 看的每条候选最大字符数
}

// MemoryConfig 三层记忆 + 长期记忆合并策略
type MemoryConfig struct {
	ShortTermMaxTurns             int
	LongTermTopK                  int
	MemoryConsolidationSimilarity float64
	MemoryConsolidationDedup      float64
	MemoryConsolidationTTLDays    int
	MemoryConsolidationDecayRate  float64
	MemoryConsolidationMinImport  float64
	MemoryConsolidationTrigger    int
}

// HarnessConfig 任务执行框架
type HarnessConfig struct {
	MaxRetries    int
	RetryDelayMs  int
	StepTimeoutMs int
	MaxIterations int
}

// SearchConfig 搜索 API（Tavily 等）
type SearchConfig struct {
	SearchAPIKey string
	SearchAPIURL string
}

// SandboxConfig 沙箱执行参数（注意：与 internal/sandbox.SandboxConfig 不是同一类型）
type SandboxConfig struct {
	SandboxEnabled     bool
	SandboxBackend     string // "docker" | "local" | "mock"
	SandboxImage       string
	SandboxTimeoutMs   int
	SandboxMaxOutput   int
	SandboxMemoryMB    int
	SandboxCPUPercent  int
	SandboxMaxPIDs     int
	SandboxNetDisabled bool
	SandboxReadOnly    bool
	// SandboxArtifactHostDir loop 产物根目录（宿主机路径，支持 ~ 展开）。
	// 每次请求在其下按 {userID}/{taskID} 建子目录并挂载到沙箱 /workspace。
	SandboxArtifactHostDir string
}

// SecurityConfig 命令安全校验
type SecurityConfig struct {
	SecMaxCmdLength  int
	SecAllowlistMode bool
	SecAllowlist     []string
}

// GraphRuntimeConfig 图运行时配置（并行调度 + 竞速 + Replan）
type GraphRuntimeConfig struct {
	GraphMaxParallel   int
	GraphRaceTimeoutMs int
	GraphEnableRacing  bool
	// Plan-and-ReAct：每层执行完让 LLM 看观察 → 决定追加/替换节点
	GraphReplanEnabled  bool
	GraphMaxReplan      int  // 单次任务最多 replan 次数（含层间 + 失败恢复），防止死循环
	GraphReplanOnFailed bool // 节点失败时是否触发 LLM 局部 replan（否则纯 retry）
}

// AuthConfig 认证 / JWT 配置。
//
// JWTSecret 必须 ≥32 字节随机；生产部署通过环境变量 / secret manager 注入，
// 不得直接写在 config.yaml 提交进 git。
// 缺失时启动期返回错误（不允许默认值），强制配置正确再启动。
type AuthConfig struct {
	JWTSecret   string
	JWTTTLHours int    // access token 有效时长（小时）；0 → 7 天默认
	JWTIssuer   string // 签发方标识，默认 "agi-assistant"
}

// ObservabilityConfig 观测/诊断相关开关。
//
// 当前仅包含 pprof 开关——生产暴露 /debug/pprof 会泄漏 goroutine 栈 / 堆信息，
// 因此默认关闭，且开启后仍要求请求带 X-Admin-Token（避免因端口误开放而全网可访问）。
// 后续 Phase 会补充 metrics / tracing 相关字段。
type ObservabilityConfig struct {
	// PprofEnabled 打开 /debug/pprof 端点；关闭时任何该前缀请求返回 404。
	PprofEnabled bool
	// PprofAdminToken 访问 pprof 时必须携带的 X-Admin-Token 值。
	// 空字符串 + PprofEnabled=true 视为配置错误——启动期直接拒绝，避免线上无鉴权暴露。
	PprofAdminToken string
}

// SkillHubConfig 控制「Skill 广场」的 GitHub 数据源。
type SkillHubConfig struct {
	// SkillHubEnabled 是否启用 GitHub 广场爬取；关闭时广场只展示内置精选。
	SkillHubEnabled bool
	// SkillHubGitHubToken 可选的 GitHub token，提升搜索 API 配额（匿名 60 次/时）。
	SkillHubGitHubToken string
	// SkillHubKeyword GitHub 搜索关键词，默认 "office productivity assistant prompt"。
	SkillHubKeyword string
	// SkillHubCacheTTLMin 搜索结果缓存分钟数，默认 30。
	SkillHubCacheTTLMin int
}

// APIConfig 整合所有阶段的 API + 基础设施配置。
//
// 所有子结构以 embedded 方式放进来，访问路径 cfg.LLMAPIUrl / cfg.PGHost
// 等通过 Go 字段提升仍然有效——重构以分层为目标，不改动调用方。
type APIConfig struct {
	ServerConfig
	LLMConfig
	EmbeddingConfig
	StorageConfig
	RAGConfig
	MemoryConfig
	HarnessConfig
	SearchConfig
	SandboxConfig
	SecurityConfig
	GraphRuntimeConfig
	AuthConfig
	ObservabilityConfig
	SkillHubConfig
}

// yamlFile 对应 config/config.yaml 的结构
type yamlFile struct {
	LLM struct {
		APIUrl      string  `yaml:"api_url"`
		APIKey      string  `yaml:"api_key"`
		Model       string  `yaml:"model"`
		FastModel   string  `yaml:"fast_model"`
		Temperature float64 `yaml:"temperature"`
	} `yaml:"llm"`
	Embedding struct {
		APIUrl string `yaml:"api_url"`
		APIKey string `yaml:"api_key"`
		Model  string `yaml:"model"`
	} `yaml:"embedding"`
	Milvus struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
	} `yaml:"milvus"`
	Postgres struct {
		Host     string `yaml:"host"`
		Port     int    `yaml:"port"`
		User     string `yaml:"user"`
		Password string `yaml:"password"`
		Database string `yaml:"database"`
	} `yaml:"postgres"`
	Elasticsearch struct {
		Addresses []string `yaml:"addresses"`
		Username  string   `yaml:"username"`
		Password  string   `yaml:"password"`
	} `yaml:"elasticsearch"`
	Kafka struct {
		Brokers []string `yaml:"brokers"`
		Topic   string   `yaml:"topic"`
	} `yaml:"kafka"`
	RAG struct {
		ChunkSize          int     `yaml:"chunk_size"`
		ChunkOverlap       int     `yaml:"chunk_overlap"`
		TopK               int     `yaml:"top_k"`
		RRFConstantK       int     `yaml:"rrf_constant_k"`
		SemanticWeight     float64 `yaml:"semantic_weight"`
		EnableHybridSearch bool    `yaml:"enable_hybrid_search"`
		RAGMilvusDim       int     `yaml:"rag_milvus_dim"`
		Rewrite            struct {
			Enabled    bool `yaml:"enabled"`
			NumQueries int  `yaml:"num_queries"`
		} `yaml:"rewrite"`
		Rerank struct {
			Enabled    bool `yaml:"enabled"`
			PreviewLen int  `yaml:"preview_len"`
		} `yaml:"rerank"`
	} `yaml:"rag"`
	Memory struct {
		ShortTermMaxTurns int `yaml:"short_term_max_turns"`
		LongTermTopK      int `yaml:"long_term_top_k"`
		Consolidation     struct {
			SimilarityThreshold float64 `yaml:"similarity_threshold"`
			DedupThreshold      float64 `yaml:"dedup_threshold"`
			TTLDays             int     `yaml:"ttl_days"`
			DecayRate           float64 `yaml:"decay_rate"`
			MinImportance       float64 `yaml:"min_importance"`
			TriggerInterval     int     `yaml:"trigger_interval"`
		} `yaml:"consolidation"`
	} `yaml:"memory"`
	Harness struct {
		MaxRetries    int `yaml:"max_retries"`
		RetryDelayMs  int `yaml:"retry_delay_ms"`
		StepTimeoutMs int `yaml:"step_timeout_ms"`
		MaxIterations int `yaml:"max_iterations"`
	} `yaml:"harness"`
	Server struct {
		Port string `yaml:"port"`
	} `yaml:"server"`
	Search struct {
		APIKey string `yaml:"api_key"`
		APIURL string `yaml:"api_url"`
	} `yaml:"search"`
	Neo4j struct {
		URI      string  `yaml:"uri"`
		User     string  `yaml:"user"`
		Password string  `yaml:"password"`
		MaxHops  int     `yaml:"max_hops"`
		Weight   float64 `yaml:"weight"`
		Enabled  bool    `yaml:"enabled"`
	} `yaml:"neo4j"`
	Sandbox struct {
		Enabled         bool   `yaml:"enabled"`
		Backend         string `yaml:"backend"`
		Image           string `yaml:"image"`
		TimeoutMs       int    `yaml:"timeout_ms"`
		MaxOutputBytes  int    `yaml:"max_output_bytes"`
		MemoryLimitMB   int    `yaml:"memory_limit_mb"`
		CPUPercent      int    `yaml:"cpu_percent"`
		MaxPIDs         int    `yaml:"max_pids"`
		NetworkDisabled bool   `yaml:"network_disabled"`
		ReadOnlyRootfs  bool   `yaml:"readonly_rootfs"`
		ArtifactHostDir string `yaml:"artifact_host_dir"`
	} `yaml:"sandbox"`
	Security struct {
		MaxCommandLength int      `yaml:"max_command_length"`
		AllowlistMode    bool     `yaml:"allowlist_mode"`
		Allowlist        []string `yaml:"allowlist"`
	} `yaml:"security"`
	GraphRuntime struct {
		MaxParallel    int  `yaml:"max_parallel"`
		RaceTimeoutMs  int  `yaml:"race_timeout_ms"`
		EnableRacing   bool `yaml:"enable_racing"`
		ReplanEnabled  bool `yaml:"replan_enabled"`
		MaxReplan      int  `yaml:"max_replan"`
		ReplanOnFailed bool `yaml:"replan_on_failed"`
	} `yaml:"graph_runtime"`
	Auth struct {
		JWTSecret string `yaml:"jwt_secret"`
		JWTTTLHrs int    `yaml:"jwt_ttl_hours"`
		JWTIssuer string `yaml:"jwt_issuer"`
	} `yaml:"auth"`
	Observability struct {
		Pprof struct {
			Enabled    bool   `yaml:"enabled"`
			AdminToken string `yaml:"admin_token"`
		} `yaml:"pprof"`
	} `yaml:"observability"`
	SkillHub struct {
		Enabled     bool   `yaml:"enabled"`
		GitHubToken string `yaml:"github_token"`
		Keyword     string `yaml:"keyword"`
		CacheTTLMin int    `yaml:"cache_ttl_min"`
	} `yaml:"skillhub"`
}

// DefaultConfig 从 config/config.yaml 加载配置
func DefaultConfig() *APIConfig {
	loadLocalEnv(".env")

	data, err := os.ReadFile("config/config.yaml")
	if err != nil {
		log.Fatalf("读取 config/config.yaml 失败: %v", err)
	}
	data = []byte(os.ExpandEnv(string(data)))

	// 严格解析：未知字段直接报错，避免 yaml 键名拼错（如 api-key vs api_key）
	// 时被静默忽略，运行时才发现 LLM/Embedding 走了 mock 模式。
	var y yamlFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&y); err != nil {
		log.Fatalf("解析 config/config.yaml 失败: %v", err)
	}

	c := &APIConfig{
		ServerConfig: ServerConfig{
			ServerPort: y.Server.Port,
		},
		LLMConfig: LLMConfig{
			LLMAPIUrl:    y.LLM.APIUrl,
			LLMAPIKey:    y.LLM.APIKey,
			LLMModel:     y.LLM.Model,
			LLMFastModel: y.LLM.FastModel,
			Temperature:  y.LLM.Temperature,
		},
		EmbeddingConfig: EmbeddingConfig{
			EmbeddingAPIUrl: y.Embedding.APIUrl,
			EmbeddingAPIKey: y.Embedding.APIKey,
			EmbeddingModel:  y.Embedding.Model,
		},
		StorageConfig: StorageConfig{
			MilvusConfig: MilvusConfig{
				MilvusHost: y.Milvus.Host,
				MilvusPort: y.Milvus.Port,
			},
			PostgresConfig: PostgresConfig{
				PGHost:     y.Postgres.Host,
				PGPort:     y.Postgres.Port,
				PGUser:     y.Postgres.User,
				PGPassword: y.Postgres.Password,
				PGDatabase: y.Postgres.Database,
			},
			ESConfig: ESConfig{
				ESAddresses: y.Elasticsearch.Addresses,
				ESUsername:  y.Elasticsearch.Username,
				ESPassword:  y.Elasticsearch.Password,
			},
			KafkaConfig: KafkaConfig{
				KafkaBrokers: y.Kafka.Brokers,
				KafkaTopic:   y.Kafka.Topic,
			},
			Neo4jConfig: Neo4jConfig{
				Neo4jURI:      y.Neo4j.URI,
				Neo4jUser:     y.Neo4j.User,
				Neo4jPassword: y.Neo4j.Password,
				KGMaxHops:     y.Neo4j.MaxHops,
				KGWeight:      y.Neo4j.Weight,
				KGEnabled:     y.Neo4j.Enabled,
			},
		},
		RAGConfig: RAGConfig{
			ChunkSize:            y.RAG.ChunkSize,
			ChunkOverlap:         y.RAG.ChunkOverlap,
			TopK:                 y.RAG.TopK,
			RRFConstantK:         y.RAG.RRFConstantK,
			SemanticWeight:       y.RAG.SemanticWeight,
			EnableHybridSearch:   y.RAG.EnableHybridSearch,
			RAGMilvusDim:         y.RAG.RAGMilvusDim,
			RAGRewriteEnabled:    y.RAG.Rewrite.Enabled,
			RAGRewriteNumQueries: y.RAG.Rewrite.NumQueries,
			RAGRerankEnabled:     y.RAG.Rerank.Enabled,
			RAGRerankPreviewLen:  y.RAG.Rerank.PreviewLen,
		},
		MemoryConfig: MemoryConfig{
			ShortTermMaxTurns:             y.Memory.ShortTermMaxTurns,
			LongTermTopK:                  y.Memory.LongTermTopK,
			MemoryConsolidationSimilarity: y.Memory.Consolidation.SimilarityThreshold,
			MemoryConsolidationDedup:      y.Memory.Consolidation.DedupThreshold,
			MemoryConsolidationTTLDays:    y.Memory.Consolidation.TTLDays,
			MemoryConsolidationDecayRate:  y.Memory.Consolidation.DecayRate,
			MemoryConsolidationMinImport:  y.Memory.Consolidation.MinImportance,
			MemoryConsolidationTrigger:    y.Memory.Consolidation.TriggerInterval,
		},
		HarnessConfig: HarnessConfig{
			MaxRetries:    y.Harness.MaxRetries,
			RetryDelayMs:  y.Harness.RetryDelayMs,
			StepTimeoutMs: y.Harness.StepTimeoutMs,
			MaxIterations: y.Harness.MaxIterations,
		},
		SearchConfig: SearchConfig{
			SearchAPIKey: y.Search.APIKey,
			SearchAPIURL: y.Search.APIURL,
		},
		SandboxConfig: SandboxConfig{
			SandboxEnabled:         y.Sandbox.Enabled,
			SandboxBackend:         y.Sandbox.Backend,
			SandboxImage:           y.Sandbox.Image,
			SandboxTimeoutMs:       y.Sandbox.TimeoutMs,
			SandboxMaxOutput:       y.Sandbox.MaxOutputBytes,
			SandboxMemoryMB:        y.Sandbox.MemoryLimitMB,
			SandboxCPUPercent:      y.Sandbox.CPUPercent,
			SandboxMaxPIDs:         y.Sandbox.MaxPIDs,
			SandboxNetDisabled:     y.Sandbox.NetworkDisabled,
			SandboxReadOnly:        y.Sandbox.ReadOnlyRootfs,
			SandboxArtifactHostDir: y.Sandbox.ArtifactHostDir,
		},
		SecurityConfig: SecurityConfig{
			SecMaxCmdLength:  y.Security.MaxCommandLength,
			SecAllowlistMode: y.Security.AllowlistMode,
			SecAllowlist:     y.Security.Allowlist,
		},
		GraphRuntimeConfig: GraphRuntimeConfig{
			GraphMaxParallel:    y.GraphRuntime.MaxParallel,
			GraphRaceTimeoutMs:  y.GraphRuntime.RaceTimeoutMs,
			GraphEnableRacing:   y.GraphRuntime.EnableRacing,
			GraphReplanEnabled:  y.GraphRuntime.ReplanEnabled,
			GraphMaxReplan:      y.GraphRuntime.MaxReplan,
			GraphReplanOnFailed: y.GraphRuntime.ReplanOnFailed,
		},
		AuthConfig: AuthConfig{
			JWTSecret:   y.Auth.JWTSecret,
			JWTTTLHours: y.Auth.JWTTTLHrs,
			JWTIssuer:   y.Auth.JWTIssuer,
		},
		ObservabilityConfig: ObservabilityConfig{
			PprofEnabled:    y.Observability.Pprof.Enabled,
			PprofAdminToken: y.Observability.Pprof.AdminToken,
		},
		SkillHubConfig: SkillHubConfig{
			SkillHubEnabled:     y.SkillHub.Enabled,
			SkillHubGitHubToken: y.SkillHub.GitHubToken,
			SkillHubKeyword:     y.SkillHub.Keyword,
			SkillHubCacheTTLMin: y.SkillHub.CacheTTLMin,
		},
	}

	applyDefaults(c)
	return c
}

// loadLocalEnv loads KEY=VALUE pairs from a local .env file without overwriting
// variables already provided by the process environment.
func loadLocalEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" || os.Getenv(key) != "" {
			continue
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if err := os.Setenv(key, value); err != nil {
			logger.L().Warn("load local env var failed", "key", key, "err", err)
		}
	}
}

// applyDefaults 为零值字段填充合理默认值
func applyDefaults(c *APIConfig) {
	// RAG 混合检索默认值
	if c.RRFConstantK <= 0 {
		c.RRFConstantK = 60
	}
	if c.SemanticWeight <= 0 {
		c.SemanticWeight = 0.7
	}
	if c.RAGMilvusDim <= 0 {
		c.RAGMilvusDim = 1024
	}

	// Query Rewrite / Rerank 默认值（默认开启，复用主 LLM 不增加依赖）
	if c.RAGRewriteNumQueries <= 0 {
		c.RAGRewriteNumQueries = 3
	}
	if c.RAGRerankPreviewLen <= 0 {
		c.RAGRerankPreviewLen = 200
	}

	// 记忆合并默认值
	if c.MemoryConsolidationSimilarity <= 0 {
		c.MemoryConsolidationSimilarity = 0.80
	}
	if c.MemoryConsolidationDedup <= 0 {
		c.MemoryConsolidationDedup = 0.95
	}
	if c.MemoryConsolidationTTLDays <= 0 {
		c.MemoryConsolidationTTLDays = 30
	}
	if c.MemoryConsolidationDecayRate <= 0 {
		c.MemoryConsolidationDecayRate = 0.995
	}
	if c.MemoryConsolidationMinImport <= 0 {
		c.MemoryConsolidationMinImport = 0.3
	}
	if c.MemoryConsolidationTrigger <= 0 {
		c.MemoryConsolidationTrigger = 5
	}

	// Neo4j 默认值
	if c.KGMaxHops <= 0 {
		c.KGMaxHops = 2
	}
	if c.KGWeight <= 0 {
		c.KGWeight = 0.3
	}

	// 沙箱默认值
	if c.SandboxBackend == "" {
		c.SandboxBackend = "docker"
	}
	if c.SandboxImage == "" {
		c.SandboxImage = "ubuntu:22.04"
	}
	if c.SandboxTimeoutMs <= 0 {
		c.SandboxTimeoutMs = 30000
	}
	if c.SandboxMaxOutput <= 0 {
		c.SandboxMaxOutput = 65536
	}
	if c.SandboxMemoryMB <= 0 {
		c.SandboxMemoryMB = 256
	}
	if c.SandboxCPUPercent <= 0 {
		c.SandboxCPUPercent = 50
	}
	if c.SandboxMaxPIDs <= 0 {
		c.SandboxMaxPIDs = 64
	}
	// 产物目录默认 ~/Desktop/agi-artifacts；展开 ~ 为用户家目录
	if c.SandboxArtifactHostDir == "" {
		c.SandboxArtifactHostDir = "~/Desktop/agi-artifacts"
	}
	c.SandboxArtifactHostDir = expandHome(c.SandboxArtifactHostDir)

	// 安全校验默认值
	if c.SecMaxCmdLength <= 0 {
		c.SecMaxCmdLength = 500
	}

	// 图运行时默认值
	if c.GraphMaxParallel <= 0 {
		c.GraphMaxParallel = 2
	}
	if c.GraphRaceTimeoutMs <= 0 {
		c.GraphRaceTimeoutMs = 30000
	}
	if c.GraphMaxReplan <= 0 {
		c.GraphMaxReplan = 2
	}

	// Skill 广场默认值
	if c.SkillHubKeyword == "" {
		// 注意：GitHub 全文搜索是多词 AND，关键词越多命中越少。
		// 用 2 个词保证有结果（4 词的 "office productivity assistant prompt" 会返回 0 条）。
		c.SkillHubKeyword = "office assistant"
	}
	if c.SkillHubCacheTTLMin <= 0 {
		c.SkillHubCacheTTLMin = 30
	}

	// 快模型未配置时回落为主模型（内部步骤与最终答案同模型，无行为变化）
	if c.LLMFastModel == "" {
		c.LLMFastModel = c.LLMModel
	}
}

// expandHome 把以 ~ 开头的路径展开为用户家目录；无法获取家目录时原样返回。
func expandHome(p string) string {
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + p[1:]
		}
	}
	return p
}

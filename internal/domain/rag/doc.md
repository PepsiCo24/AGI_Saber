完整流程（顶层文件 rag.go 的 Engine 编排）：

	Ingest:
	  ParentSplitter (大块) ── 子块 RecursiveSplitter ──► PG / Milvus / ES + Neo4j

	Query:
	  Rewriter (history-aware + multi-query)
	      │
	      ▼
	  HybridStore.SearchMulti（每条 query 三路 RRF → 跨查询 RRF 合并）
	      │
	      ▼
	  Reranker (LLM listwise 精排，可关闭)
	      │
	      ▼
	  父块回填 (small-to-big) ──► LLM 合成

文件分组：

	rag.go        Engine 编排
	splitter.go   字符滑窗 + 递归切分（Markdown 感知 + 代码块保护）
	hybrid.go     HybridStore：Milvus 语义 + ES BM25 + Neo4j 图 + RRF 融合
	rewriter.go   Rewriter 接口 + LLM 改写实现
	reranker.go   Reranker 接口 + LLM listwise 实现

学习入口建议：先看 splitter（理解父子块结构），再看 hybrid（RRF 融合是核心），
最后看 rewriter / reranker（增强层，可关闭）。

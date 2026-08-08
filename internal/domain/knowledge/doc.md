Package knowledge 知识图谱（RAG 文档实体关系）。

由 LLM 从文档抽取实体（Entity）和关系（Relation），存入 Neo4j。
检索时根据 query 命中实体做 1~2 跳子图遍历，作为 RAG 三路检索的图谱通道。
文件分组：

types.go      Entity / EntityType / Relation / GraphSearchResult / ExtractResult
extractor.go  通过 LLM 抽取实体关系（强约束 7 种实体 + 7 种关系类型）
kgstore.go    KGStore：IndexDocument / Search（含 APOC 子图扩展和降级路径）

与 memory/graph 的区别：
- knowledge 处理"文档实体"（Entity 节点）—— 出现在哪些 chunk
- memory/graph 处理"记忆条目"（Memory 节点）—— 时序 + 相似度连接
- 二者共享同一 Neo4j 实例，但 schema 不同（节点 label 不同）

import json
from types import SimpleNamespace

from internal.rag.hybrid import HybridResult, HybridStore
from internal.rag.rag import Engine
from internal.rag.reranker import LLMReranker
from internal.rag.rewriter import HistoryMessage, LLMRewriter
from internal.rag.splitter import RecursiveSplitter


def test_recursive_splitter_creates_overlapping_chunks():
    splitter = RecursiveSplitter(chunk_size=10, chunk_overlap=3)

    chunks = splitter.split("abcdefghijklmnopqrstuvwxyz")

    assert [c.id for c in chunks] == list(range(len(chunks)))
    # 无任何语义分隔符 → 进入兜底硬切，再叠 tail-rune overlap
    assert chunks[0].content == "abcdefghij"
    for i in range(1, len(chunks)):
        prev_tail = chunks[i - 1].content[-3:]
        assert chunks[i].content.startswith(prev_tail)
    assert "".join(c.content[3:] if i else c.content
                   for i, c in enumerate(chunks)) == "abcdefghijklmnopqrstuvwxyz"


def test_recursive_splitter_basic():
    text = (
        "第一段开头。第一段中间。第一段结尾。\n\n"
        "第二段第一句。第二段第二句。第二段第三句。\n\n"
        "第三段只有一句。"
    )
    splitter = RecursiveSplitter(chunk_size=20, chunk_overlap=0)

    chunks = splitter.split(text)

    assert chunks, "应至少产出一个 chunk"
    for c in chunks:
        assert len(c.content) <= 20, f"chunk 超长: {c.content!r}"
    joined = "".join(c.content for c in chunks)
    # 递归切只删去 strip 后为空的纯空白片段，正文字符应保留
    for ch in "第一段中间第二段第三句":
        assert ch in joined


def test_recursive_splitter_code_fence():
    code = "```python\ndef foo():\n    return 'a very long line that exceeds the chunk size easily'\n```"
    text = f"前置说明文本。\n\n{code}\n\n后置说明文本。"
    splitter = RecursiveSplitter(chunk_size=30, chunk_overlap=0)

    chunks = splitter.split(text)
    contents = [c.content for c in chunks]

    assert any(code in c for c in contents), \
        f"代码块应作为原子片段完整保留，实际 chunks={contents!r}"
    # 确认代码块没有被中间切开：不存在仅含部分 fence 的 chunk
    for c in contents:
        if "```" in c:
            assert c.count("```") % 2 == 0, f"代码块被截断: {c!r}"


def test_recursive_splitter_chinese_overlap():
    # 含中文 + emoji（部分 emoji 在 UTF-16 下是代理对，Python str 按 code point 切）
    text = "你好世界🌍🚀。今天的天气真不错。我们一起去公园散步吧。再聊聊技术话题哦。最后一段尾声。"
    splitter = RecursiveSplitter(chunk_size=15, chunk_overlap=3)

    chunks = splitter.split(text)

    assert len(chunks) >= 2
    # Python str 切片天然按 code point，不会出现半字
    for c in chunks:
        assert len(c.content) >= 1
        for ch in c.content:
            assert isinstance(ch, str) and len(ch) == 1
    # 相邻 chunk 必须存在 rune 级 overlap：cur 的开头与 prev 的某个后缀完整相等
    for i in range(1, len(chunks)):
        prev = chunks[i - 1].content
        cur = chunks[i].content
        max_n = min(splitter.chunk_overlap, len(prev), len(cur))
        assert max_n > 0
        assert any(prev.endswith(cur[:n]) and n > 0 for n in range(1, max_n + 1)), (
            f"overlap 未按 rune 对齐: prev_tail={prev[-max_n:]!r} cur_head={cur[:max_n]!r}"
        )
    # emoji 必须保留为完整 code point
    joined = "".join(c.content for c in chunks)
    assert "🌍" in joined and "🚀" in joined


def test_rewriter_returns_deduplicated_queries_from_json():
    def generate(system_prompt, user_msg):
        assert "最近对话历史" in user_msg
        assert "当前问题：那个怎么实现" in user_msg
        return json.dumps({"queries": ["图式 Runtime 怎么实现", "DAG Runtime 实现", "图式 Runtime 怎么实现"]})

    rewriter = LLMRewriter(generate, num_queries=3)

    queries = rewriter.rewrite(
        "那个怎么实现",
        [HistoryMessage(role="user", content="我们刚刚在讨论图式 Runtime")],
    )

    assert queries == ["图式 Runtime 怎么实现", "DAG Runtime 实现", "那个怎么实现"]


def test_rewriter_falls_back_to_original_query_on_bad_json():
    rewriter = LLMRewriter(lambda _system, _user: "not json", num_queries=3)

    assert rewriter.rewrite("查询 RAG", []) == ["查询 RAG"]


class _Result:
    def __init__(self, content, score):
        self.content = content
        self.score = score
        self.source = "hybrid"


def test_reranker_orders_by_llm_score_and_marks_source():
    def generate(_system_prompt, user_msg):
        assert "[0] weak" in user_msg
        assert "[1] strong" in user_msg
        return json.dumps({"scores": [{"idx": 0, "score": 2}, {"idx": 1, "score": 9}]})

    reranker = LLMReranker(generate, preview_len=100)
    results = [_Result("weak", 0.9), _Result("strong", 0.1)]

    reranked = reranker.rerank("question", results, top_k=2)

    assert [r.content for r in reranked] == ["strong", "weak"]
    assert [r.score for r in reranked] == [0.9, 0.2]
    assert all(r.source.endswith("+rerank") for r in reranked)


def test_reranker_falls_back_to_rrf_order_on_bad_json():
    reranker = LLMReranker(lambda _system, _user: "bad", preview_len=100)
    results = [_Result("first", 0.7), _Result("second", 0.6)]

    reranked = reranker.rerank("question", results, top_k=1)

    assert [r.content for r in reranked] == ["first"]


class _FakeRagchunkRepo:
    def __init__(self, infra):
        self.infra = infra

    def count(self):
        return len(self.infra.saved_chunks)

    def load_by_ids_with_parent(self, ids):
        return [self.infra.rows[i] for i in ids if i in self.infra.rows]

    def search_milvus_dicts(self, _embedding, _top_k):
        return [{"pg_id": 1, "score": 0.9}, {"pg_id": 2, "score": 0.8}]

    def search_es_dicts(self, query, _top_k):
        if "alt" in query:
            return [{"pg_id": 2, "score": 10.0}, {"pg_id": 3, "score": 9.0}]
        return [{"pg_id": 1, "score": 10.0}, {"pg_id": 3, "score": 8.0}]

    def save_pg_with_parent(self, doc_hash, chunk_idx, content, parent_content, embedding_json):
        pg_id = self.infra.next_id
        self.infra.next_id += 1
        self.infra.saved_chunks.append({
            "id": pg_id,
            "doc_hash": doc_hash,
            "chunk_idx": chunk_idx,
            "content": content,
            "parent_content": parent_content,
            "embedding_json": embedding_json,
        })
        self.infra.rows[pg_id] = {"id": pg_id, "content": content, "parent_content": parent_content}
        return pg_id

    def save_pg(self, doc_hash, chunk_idx, content, embedding_json):
        return self.save_pg_with_parent(doc_hash, chunk_idx, content, "", embedding_json)

    def insert_milvus(self, pg_ids, contents, embeddings):
        self.infra.inserted_milvus.append((pg_ids, contents, embeddings))

    def index_es(self, pg_id, content, doc_hash, chunk_idx):
        self.infra.indexed_chunks.append({
            "pg_id": pg_id,
            "content": content,
            "doc_hash": doc_hash,
            "chunk_idx": chunk_idx,
        })


class _FakeEventsRepo:
    def __init__(self, infra):
        self.infra = infra

    def publish(self, event_type, payload):
        self.infra.events.append((event_type, payload))


class _FakeInfra:
    def __init__(self):
        self.ready = SimpleNamespace(postgresql="connected", milvus="connected", elasticsearch="connected")
        self.saved_chunks = []
        self.indexed_chunks = []
        self.inserted_milvus = []
        self.rows = {
            1: {"id": 1, "content": "child a", "parent_content": "parent A"},
            2: {"id": 2, "content": "child b", "parent_content": "parent B"},
            3: {"id": 3, "content": "child c", "parent_content": ""},
        }
        self.next_id = 10
        self.events = []
        self.repo = SimpleNamespace(
            ragchunk=_FakeRagchunkRepo(self),
            events=_FakeEventsRepo(self),
        )


class _FakeCfg:
    chunk_size = 10
    chunk_overlap = 0
    top_k = 2
    rrf_constant_k = 60
    semantic_weight = 0.5
    kg_weight = 0.0
    kg_enabled = False
    enable_hybrid_search = True
    rag_milvus_dim = 3

    def is_real_embedding(self):
        return True


class _FakeLLM:
    cfg = _FakeCfg()

    def embed(self, _text):
        return [0.1, 0.2, 0.3]


def test_hybrid_search_multi_merges_queries_and_uses_reranker():
    store = HybridStore(_FakeCfg(), _FakeInfra(), embed_fn=lambda _q: [0.1, 0.2, 0.3])
    store.set_reranker(LLMReranker(
        lambda _system, _user: json.dumps({"scores": [{"idx": 0, "score": 1}, {"idx": 1, "score": 10}]}),
        preview_len=100,
    ))

    results = store.search_multi(["main", "alt"], top_k=2)

    assert [r.pg_id for r in results] == [2, 1]
    assert results[0].parent == "parent B"
    assert results[0].source.endswith("+rerank")


def test_engine_ingest_saves_child_chunks_with_parent_content():
    inf = _FakeInfra()
    engine = Engine(_FakeCfg(), inf, _FakeLLM())

    count = engine.ingest("abcdefghijklmnopqrstuvwxyz")

    assert count == len(inf.saved_chunks)
    assert any(row["parent_content"] != row["content"] for row in inf.saved_chunks)
    assert all(row["parent_content"] for row in inf.saved_chunks)


def test_engine_ingest_writes_to_kg_when_available():
    """ingest 应在 KGStore 可用时同步把每个有效 chunk 写入 Neo4j。

    回归此前的隐性 bug：KGStore.search 早已实现，但 ingest 从未触发
    KGStore.index_document，导致 Neo4j 始终为空、_fetch_kg 永远返回空结果。
    """
    from internal.graph.types import ChunkRef

    class _FakeKG:
        def __init__(self, available=True):
            self._available = available
            self.calls = []

        def available(self):
            return self._available

        def index_document(self, doc_hash, refs):
            self.calls.append((doc_hash, list(refs)))

    inf = _FakeInfra()
    engine = Engine(_FakeCfg(), inf, _FakeLLM())
    kg = _FakeKG(available=True)
    engine.set_kg_store(kg)

    engine.ingest("abcdefghijklmnopqrstuvwxyz")

    assert len(kg.calls) == 1, "KGStore.index_document should be called exactly once"
    doc_hash, refs = kg.calls[0]
    assert isinstance(doc_hash, str) and len(doc_hash) == 16
    assert refs and all(isinstance(r, ChunkRef) for r in refs)
    assert all(r.pg_id > 0 and r.content for r in refs)


def test_engine_ingest_skips_kg_when_unavailable():
    """KGStore.available()=False 时不应调用 index_document，避免 Neo4j 故障阻塞主入库。"""

    class _FakeKG:
        def __init__(self):
            self.calls = []

        def available(self):
            return False

        def index_document(self, doc_hash, refs):
            self.calls.append((doc_hash, refs))

    inf = _FakeInfra()
    engine = Engine(_FakeCfg(), inf, _FakeLLM())
    kg = _FakeKG()
    engine.set_kg_store(kg)

    engine.ingest("abcdefghijklmnopqrstuvwxyz")

    assert kg.calls == [], "KG should be skipped when not available"


def test_engine_query_with_history_uses_rewrite_search_multi_and_parent_context():
    inf = _FakeInfra()
    engine = Engine(_FakeCfg(), inf, _FakeLLM())
    engine.loaded = True
    engine.set_rewriter(LLMRewriter(lambda _system, _user: json.dumps({"queries": ["main", "alt"]}), 2))
    captured = {}

    def generate(_system, user_msg):
        captured["user_msg"] = user_msg
        return "answer"

    engine.set_generate_fn(generate)

    answer, results = engine.query_with_history("原问题", [HistoryMessage(role="user", content="历史")])

    assert answer == "answer"
    assert "parent A" in captured["user_msg"]
    assert "parent B" in captured["user_msg"]
    assert [r["content"] for r in results] == ["parent A", "parent B"]


class _FailingEmbedLLM:
    cfg = _FakeCfg()

    def embed(self, _text):
        raise RuntimeError("embedding service down")


def test_engine_ingest_saves_pg_and_es_when_embedding_fails():
    inf = _FakeInfra()
    engine = Engine(_FakeCfg(), inf, _FailingEmbedLLM())

    count = engine.ingest("abcdefghijklmnopqrstuvwxyz")

    assert count == len(inf.saved_chunks)
    assert len(inf.indexed_chunks) == count
    assert inf.inserted_milvus == []
    assert all(row["embedding_json"] == "[]" for row in inf.saved_chunks)


def test_engine_compose_answer_deduplicates_same_display_content():
    engine = Engine(_FakeCfg(), _FakeInfra(), _FakeLLM())

    answer, results = engine._compose_answer("question", [
        {"pg_id": 1, "content": "same parent", "score": 0.9, "source": "keyword"},
        {"pg_id": 2, "content": "same parent", "score": 0.8, "source": "semantic"},
        {"pg_id": 3, "content": "other parent", "score": 0.7, "source": "keyword"},
    ])

    assert "same parent" in answer
    assert [r["content"] for r in results] == ["same parent", "other parent"]


def test_llm_embed_does_not_return_mock_vector_when_unconfigured():
    from config.config import APIConfig
    from internal.llm.llm import Client

    client = Client(APIConfig())

    try:
        client.embed("hello")
    except RuntimeError as e:
        assert "未配置" in str(e)
    else:
        raise AssertionError("embed should raise instead of returning a mock vector")


def test_save_rag_chunk_with_parent_is_idempotent_upsert():
    """重复 ingest 同一 (doc_hash, chunk_idx) 不应触发 UNIQUE 冲突。

    对齐 main 分支 Go 实现 (internal/infrastructure/persistence/ragchunk/ragchunk.go
    SavePGWithParent)：使用 ON CONFLICT (doc_hash, chunk_idx) DO UPDATE，
    返回的 id 应保持稳定。
    """
    from internal.repo.ragchunk import Store

    executed = []

    class FakeCursor:
        def __enter__(self):
            return self

        def __exit__(self, *_a):
            return False

        def execute(self, sql, params=None):
            executed.append((sql, params))

        def fetchone(self):
            return (42,)

    class FakeConn:
        def cursor(self):
            return FakeCursor()

    class FakePG:
        def __init__(self, conn):
            self.conn = conn

        def is_real(self):
            return True

    store = Store(FakePG(FakeConn()), None, None)

    id1 = store.save_pg_with_parent("doc-hash-x", 0, "c1", "p1", "[]")
    id2 = store.save_pg_with_parent("doc-hash-x", 0, "c2", "p2", "[]")

    assert id1 == 42 and id2 == 42, "重复写入应返回相同的 id"
    assert len(executed) == 2
    for sql, params in executed:
        assert "ON CONFLICT" in sql
        assert "(doc_hash, chunk_idx)" in sql
        assert "EXCLUDED.content" in sql
        assert "EXCLUDED.parent_content" in sql
        assert "EXCLUDED.embedding" in sql
        assert "RETURNING id" in sql
        assert params is not None and params[0] == "doc-hash-x" and params[1] == 0


def test_save_rag_chunk_falls_back_when_conflict_target_unavailable():
    """旧库唯一约束异常时，PG 保存应回退到 select/update/insert 路径。"""
    from internal.repo.ragchunk import Store

    executed = []

    class FakeCursor:
        def __enter__(self):
            return self

        def __exit__(self, *_a):
            return False

        def execute(self, sql, params=None):
            executed.append((sql, params))
            if "ON CONFLICT" in sql:
                raise RuntimeError("there is no unique or exclusion constraint matching the ON CONFLICT specification")

        def fetchone(self):
            if len(executed) == 2:
                return (42,)
            return None

    class FakeConn:
        def cursor(self):
            return FakeCursor()

    class FakePG:
        def __init__(self, conn):
            self.conn = conn

        def is_real(self):
            return True

    store = Store(FakePG(FakeConn()), None, None)

    pg_id = store.save_pg_with_parent("doc-hash-y", 1, "content", "parent", "[]")

    assert pg_id == 42
    assert any("SELECT id FROM rag_chunks" in sql for sql, _ in executed)
    assert not any("INSERT INTO rag_chunks" in sql and "ON CONFLICT" not in sql for sql, _ in executed)

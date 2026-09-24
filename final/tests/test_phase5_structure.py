import inspect

from config.config import APIConfig
from internal.rag.hybrid import HybridStore
from internal.rag.rag import Engine
from internal.sandbox.factory import create_sandbox


def test_removed_legacy_rag_helpers_are_absent():
    assert not hasattr(Engine, "_rrf_fuse")
    assert not hasattr(HybridStore, "_normalized_weights")
    assert not hasattr(HybridStore, "_materialize_kg_only")


def test_engine_query_uses_hybrid_store_search_multi_without_legacy_fallback():
    source = inspect.getsource(Engine.query_with_history)

    assert "search_multi" in source
    assert "_rrf_fuse" not in source
    assert "search_milvus_dicts" not in source
    assert "search_es_dicts" not in source


def test_hybrid_store_no_longer_exposes_removed_helpers():
    assert not hasattr(HybridStore, "_normalized_weights")
    assert not hasattr(HybridStore, "_materialize_kg_only")


def test_sandbox_factory_creates_mock_sandbox():
    cfg = APIConfig()
    cfg.sandbox_backend = "mock"

    sandbox = create_sandbox(cfg)

    assert sandbox.backend() == "mock"


def test_main_exposes_deps_container_and_builder():
    import main

    assert hasattr(main, "Deps")
    assert hasattr(main, "build_deps")
    fields = set(getattr(main.Deps, "__dataclass_fields__", {}).keys())
    assert {"cfg", "inf", "agent", "app"}.issubset(fields)

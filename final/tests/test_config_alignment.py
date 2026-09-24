import pytest

from config.config import default_config


def test_alignment_config_defaults(tmp_path):
    config_path = tmp_path / "empty.yaml"
    config_path.write_text("", encoding="utf-8")

    cfg = default_config(str(config_path))

    assert cfg.rag_rewrite_enabled is False
    assert cfg.rag_rewrite_num_queries == 3
    assert cfg.rag_rerank_enabled is False
    assert cfg.rag_rerank_preview_len == 200
    assert cfg.graph_max_parallel == 2
    assert cfg.graph_race_timeout_ms == 30000
    assert cfg.graph_enable_racing is True


def test_alignment_config_reads_rag_and_graph_runtime(tmp_path):
    config_path = tmp_path / "config.yaml"
    config_path.write_text(
        """
rag:
  rewrite:
    enabled: true
    num_queries: 4
  rerank:
    enabled: true
    preview_len: 320
graph_runtime:
  max_parallel: 5
  race_timeout_ms: 1234
  enable_racing: false
""",
        encoding="utf-8",
    )

    cfg = default_config(str(config_path))

    assert cfg.rag_rewrite_enabled is True
    assert cfg.rag_rewrite_num_queries == 4
    assert cfg.rag_rerank_enabled is True
    assert cfg.rag_rerank_preview_len == 320
    assert cfg.graph_max_parallel == 5
    assert cfg.graph_race_timeout_ms == 1234
    assert cfg.graph_enable_racing is False


def test_config_rejects_unknown_top_level_field(tmp_path):
    config_path = tmp_path / "config.yaml"
    config_path.write_text("lllm:\n  model: typo\n", encoding="utf-8")

    with pytest.raises(ValueError, match="unknown config field"):
        default_config(str(config_path))


def test_config_rejects_unknown_nested_field(tmp_path):
    config_path = tmp_path / "config.yaml"
    config_path.write_text(
        """
rag:
  chunk_size: 200
  chunk_szie: 999
""",
        encoding="utf-8",
    )

    with pytest.raises(ValueError, match="rag.chunk_szie"):
        default_config(str(config_path))


def test_default_config_prefers_local_config(monkeypatch, tmp_path):
    project_root = tmp_path / "project"
    config_dir = project_root / "config"
    config_dir.mkdir(parents=True)
    (config_dir / "config.yaml").write_text(
        "server:\n  port: 8000\n",
        encoding="utf-8",
    )
    (config_dir / "config.local.yaml").write_text(
        "server:\n  port: 9001\n",
        encoding="utf-8",
    )

    monkeypatch.setenv("AGI_PROJECT_ROOT", str(project_root))

    cfg = default_config()

    assert cfg.server_port == "9001"

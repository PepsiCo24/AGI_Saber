"""Preference 独立模块的单元测试。

覆盖：
- extract_and_save 三条规则（我喜欢 / 我爱 / 我叫）+ 未命中
- build_context 渲染【用户偏好】块；空数据返回空串
- save / save_batch 经过 inf.repo.preference 持久化
- 多线程并发 set/get_all 不抛异常
"""
import threading
from types import SimpleNamespace
from typing import Dict, List, Tuple

from internal.memory.preference import Preference


class _PrefRepo:
    def __init__(self):
        self.saved: List[Tuple[str, str, str]] = []
        self.store: Dict[str, str] = {}

    def load(self, user_id: str) -> Dict[str, str]:
        return dict(self.store)

    def save(self, user_id: str, key: str, value: str) -> None:
        self.saved.append((user_id, key, value))
        self.store[key] = value


def _make() -> Preference:
    repo = _PrefRepo()
    inf = SimpleNamespace(repo=SimpleNamespace(preference=repo))
    p = Preference("u1", inf)
    p._repo_ref = repo  # 测试用引用
    return p


def test_extract_and_save_likes():
    p = _make()
    key, value, ok = p.extract_and_save("我喜欢喝拿铁")
    assert ok
    assert key == "喜好"
    assert value == "喝拿铁"
    assert p.get("喜好") == "喝拿铁"
    # 持久化路径
    assert ("u1", "喜好", "喝拿铁") in p._repo_ref.saved


def test_extract_and_save_love():
    p = _make()
    key, value, ok = p.extract_and_save("我爱跑步")
    assert ok
    assert key == "喜好"
    assert value == "跑步"


def test_extract_and_save_name():
    p = _make()
    key, value, ok = p.extract_and_save("我叫小明")
    assert ok
    assert key == "姓名"
    assert value == "小明"


def test_extract_and_save_no_match():
    p = _make()
    key, value, ok = p.extract_and_save("今天天气不错")
    assert not ok
    assert key == ""
    assert value == ""


def test_extract_and_save_empty_value():
    p = _make()
    # "我叫" 后面没有内容
    key, value, ok = p.extract_and_save("我叫")
    assert not ok


def test_build_context_empty():
    p = _make()
    assert p.build_context() == ""


def test_build_context_renders_lines():
    p = _make()
    p.set("姓名", "张三")
    p.set("喜好", "咖啡")
    ctx = p.build_context()
    assert ctx.startswith("【用户偏好】\n")
    assert "姓名: 张三" in ctx
    assert "喜好: 咖啡" in ctx


def test_save_batch_persists():
    p = _make()
    p.save_batch({"姓名": "李四", "喜好": "茶"})
    assert p.get("姓名") == "李四"
    assert p.get("喜好") == "茶"
    saved_keys = {k for _, k, _ in p._repo_ref.saved}
    assert {"姓名", "喜好"} <= saved_keys


def test_concurrent_set_and_get_all_is_safe():
    p = _make()
    errors: list = []

    def writer(idx: int):
        try:
            for i in range(100):
                p.set(f"k{idx}-{i}", f"v{i}")
        except Exception as e:
            errors.append(e)

    def reader():
        try:
            for _ in range(100):
                _ = p.get_all()
        except Exception as e:
            errors.append(e)

    threads = [threading.Thread(target=writer, args=(i,)) for i in range(3)]
    threads += [threading.Thread(target=reader) for _ in range(2)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    assert errors == []
    assert len(p.get_all()) == 3 * 100


def test_legacy_alias_still_works():
    """`internal.memory.memory.Preference` 别名应该构造出真实 Preference。"""
    from internal.memory.memory import Preference as Alias
    repo = _PrefRepo()
    inf = SimpleNamespace(repo=SimpleNamespace(preference=repo))
    p = Alias("u2", inf)
    assert isinstance(p, Preference)
    p.set("k", "v")
    assert p.get("k") == "v"

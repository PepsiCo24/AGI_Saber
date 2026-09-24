"""CancelRegistry 多任务管理 + 快照防泄漏 单元测试。

对齐 main 分支 taskRuntime（runtime_task.go）：
- registerCancel/cancelAll：多 in-flight token 全部触发
- setTask：清空 snapshots
- appendSnapshot/snapshotList：持锁、返回拷贝
"""

import threading

from internal.agent.cancel import CancelRegistry, CancelToken, go_safe


def test_register_and_unregister_does_not_leak():
    reg = CancelRegistry()
    for _ in range(100):
        token, unregister = reg.register()
        assert isinstance(token, CancelToken)
        unregister()
    # 100 轮注册后 _tokens 应已全部清空，避免泄漏
    assert len(reg._tokens) == 0


def test_unregister_cancels_token_idempotent():
    reg = CancelRegistry()
    token, unregister = reg.register()
    assert not token.is_cancelled()
    unregister()
    assert token.is_cancelled()
    # 二次 unregister 幂等
    unregister()
    assert token.is_cancelled()


def test_cancel_all_triggers_all_in_flight_tokens():
    reg = CancelRegistry()
    pairs = [reg.register() for _ in range(5)]
    tokens = [t for t, _u in pairs]
    assert all(not t.is_cancelled() for t in tokens)

    reg.cancel_all()
    assert all(t.is_cancelled() for t in tokens)
    # cancel_all 不会清表，由各 unregister 各自 pop
    for _t, u in pairs:
        u()
    assert len(reg._tokens) == 0


def test_set_task_clears_snapshots():
    reg = CancelRegistry()
    reg.set_task({"task_id": "t1"})
    reg.append_snapshot({"step": 1})
    reg.append_snapshot({"step": 2})
    assert len(reg.snapshot_list()) == 2

    # 切换任务时 snapshots 清空（对应 Go setTask）
    reg.set_task({"task_id": "t2"})
    assert reg.snapshot_list() == []
    assert reg.current_task() == {"task_id": "t2"}


def test_snapshot_list_returns_copy():
    reg = CancelRegistry()
    reg.append_snapshot({"step": 1})
    snap = reg.snapshot_list()
    snap.append({"step": "external mutation"})
    # 外部修改不影响内部
    assert reg.snapshot_list() == [{"step": 1}]


def test_append_snapshot_concurrent_writes():
    reg = CancelRegistry()
    reg.set_task({"task_id": "concurrent"})

    def writer(start: int):
        for i in range(50):
            reg.append_snapshot({"id": start + i})

    threads = [threading.Thread(target=writer, args=(k * 1000,)) for k in range(4)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    assert len(reg.snapshot_list()) == 200


def test_concurrent_register_unregister_no_race():
    reg = CancelRegistry()

    def loop():
        for _ in range(200):
            _t, u = reg.register()
            u()

    threads = [threading.Thread(target=loop) for _ in range(8)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    assert len(reg._tokens) == 0


def test_go_safe_recovers_from_exception():
    """go_safe 抛异常的子线程不应影响调用方（已知行为，回归保护）。"""
    done = threading.Event()

    def boom():
        try:
            raise RuntimeError("boom")
        finally:
            done.set()

    t = go_safe("test-boom", boom)
    t.join(timeout=1.0)
    assert done.is_set()

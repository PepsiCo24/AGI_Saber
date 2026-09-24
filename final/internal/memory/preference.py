# preference — 用户偏好独立模块（与 main 分支 internal/domain/memory/preference 对齐）。
#
# 与 main 分支 Go Preference 的差异：
#   - Python 版仍然持有 ``user_id`` 与 ``inf``，因为现有 repo 接口按 user_id 维度
#     做读写；main 分支由更高层装配 user_id，这里出于历史原因保持现状不动。
#   - ExtractAndSave 规则、BuildContext 输出格式、对外方法签名严格对齐。
import logging
import threading
from typing import Dict, Optional, Tuple

from internal.infra.infra import Infrastructure

logger = logging.getLogger(__name__)


class Preference:
    """用户偏好管理。线程安全：data 读写均走 RLock。"""

    def __init__(self, user_id: str, inf: Infrastructure):
        self.user_id = user_id
        self.inf = inf
        self.preferences: Dict[str, str] = {}
        self._lock = threading.RLock()
        self.load_from_storage()

    @property
    def data(self) -> Dict[str, str]:
        return self.preferences

    def load_from_storage(self) -> None:
        loaded = self.inf.repo.preference.load(self.user_id) or {}
        with self._lock:
            self.preferences = dict(loaded)
        logger.info("✅ 加载用户 %s 的偏好: %s", self.user_id, loaded)

    def set(self, key: str, value: str) -> None:
        if not key or value is None:
            return
        with self._lock:
            self.preferences[key] = value
        self.inf.repo.preference.save(self.user_id, key, value)

    def save_batch(self, kvs: Dict[str, str]) -> None:
        for k, v in (kvs or {}).items():
            self.set(str(k), str(v))

    def get(self, key: str, default: str = "") -> str:
        with self._lock:
            return self.preferences.get(key, default)

    def get_all(self) -> Dict[str, str]:
        with self._lock:
            return dict(self.preferences)

    def snapshot(self) -> Dict[str, str]:
        return self.get_all()

    # ─── main 分支对齐 ─────────────────────────────────────────────────────

    def extract_and_save(self, msg: str) -> Tuple[str, str, bool]:
        """从用户输入提取偏好并落库。

        与 main 分支 Go ExtractAndSave 严格对齐：仅识别 "我喜欢" / "我爱" / "我叫"
        三条规则，``strings.SplitN(msg, "X", 2)`` 取分隔符之后的部分；任一规则
        提取出非空 value 即写入；未命中返回 ("", "", False)。
        """
        if not msg:
            return "", "", False

        rules = [
            ("我喜欢", "喜欢", "喜好"),
            ("我爱", "爱", "喜好"),
            ("我叫", "叫", "姓名"),
        ]
        for marker, sep, key in rules:
            if marker not in msg:
                continue
            parts = msg.split(sep, 1)
            if len(parts) < 2:
                continue
            value = parts[1].strip()
            if not value:
                continue
            self.set(key, value)
            return key, value, True
        return "", "", False

    def build_context(self) -> str:
        """渲染【用户偏好】块；空数据返回空串。

        输出格式：首行 "【用户偏好】"，随后每行 "key: value"。
        """
        snap = self.snapshot()
        if not snap:
            return ""
        lines = [f"{k}: {v}" for k, v in snap.items()]
        return "【用户偏好】\n" + "\n".join(lines)


__all__ = ["Preference"]

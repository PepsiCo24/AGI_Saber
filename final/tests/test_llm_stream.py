"""chat_stream_context 真流式接口单测。

覆盖三类场景：
1. mock 模式：按字符 sleep 推送，累积值等于完整 mock 答复。
2. 真实 API 模式：用 fake response 模拟 OpenAI 兼容 SSE，断言 on_token 被多次
   调用、累积值匹配预期、``[DONE]`` 后退出。
3. cancel 透传：ctx.is_cancelled() 触发后 session 被 close。
"""
import threading
import time
from types import SimpleNamespace
from typing import List

import pytest

from internal.llm.llm import Client, Message


def _mock_cfg(real: bool = False) -> SimpleNamespace:
    return SimpleNamespace(
        llm_api_url="https://example.com/v1/chat/completions",
        llm_api_key="fake-key" if real else "",
        llm_model="fake-model",
        temperature=0.5,
        embedding_api_url="",
        embedding_api_key="",
        embedding_model="",
        is_real_llm=lambda: real,
        is_real_embedding=lambda: False,
    )


class _FakeStreamResponse:
    """模拟 requests.Response，提供 status_code / iter_lines / text。"""

    def __init__(self, lines: List[str], status_code: int = 200):
        self.status_code = status_code
        self._lines = lines
        self.text = "\n".join(lines)

    def iter_lines(self, decode_unicode: bool = False):  # noqa: D401
        for ln in self._lines:
            yield ln

    def close(self):
        pass


class _FakeSession:
    last: "_FakeSession | None" = None

    def __init__(self, response: _FakeStreamResponse, on_post=None):
        self._response = response
        self._on_post = on_post
        self.closed = False
        _FakeSession.last = self

    def post(self, url, headers=None, json=None, stream=False, timeout=None):
        if self._on_post:
            self._on_post(url, headers, json, stream, timeout)
        return self._response

    def close(self):
        self.closed = True


def test_chat_stream_context_mock_emits_per_char():
    cfg = _mock_cfg(real=False)
    client = Client(cfg)
    tokens: List[str] = []
    full = client.chat_stream_context(
        ctx=None,
        system_prompt="",
        messages=[Message(role="user", content="你是谁")],
        on_token=lambda t: tokens.append(t),
    )
    assert full == "我是一个全能 AI 助手，具备知识库、工具调用、推理、记忆和稳定执行能力。"
    assert "".join(tokens) == full
    assert len(tokens) == len(full)


def test_chat_stream_context_parses_openai_sse(monkeypatch):
    cfg = _mock_cfg(real=True)
    client = Client(cfg)

    sse_lines = [
        'data: {"choices":[{"delta":{"content":"你好"}}]}',
        '',
        'data: {"choices":[{"delta":{"content":"，"}}]}',
        'data: {"choices":[{"delta":{"content":"世界"}}]}',
        'data: {"choices":[{"delta":{"content":"！"}}]}',
        'data: [DONE]',
        'data: {"choices":[{"delta":{"content":"漏过"}}]}',
    ]
    fake_resp = _FakeStreamResponse(sse_lines)

    captured = {}

    def _on_post(url, headers, payload, stream, timeout):
        captured["url"] = url
        captured["payload"] = payload
        captured["stream"] = stream
        captured["timeout"] = timeout
        captured["auth"] = headers.get("Authorization") if headers else None

    def _factory():
        return _FakeSession(fake_resp, on_post=_on_post)

    import internal.llm.llm as llm_mod
    monkeypatch.setattr(llm_mod.requests, "Session", _factory)

    tokens: List[str] = []
    full = client.chat_stream_context(
        ctx=None,
        system_prompt="sys-prompt",
        messages=[Message(role="user", content="hi")],
        on_token=lambda t: tokens.append(t),
    )

    assert tokens == ["你好", "，", "世界", "！"]
    assert full == "你好，世界！"
    assert captured["stream"] is True
    assert captured["payload"]["stream"] is True
    assert captured["payload"]["model"] == "fake-model"
    assert captured["payload"]["messages"][0] == {"role": "system", "content": "sys-prompt"}
    assert captured["payload"]["messages"][1] == {"role": "user", "content": "hi"}
    assert captured["timeout"] == (10, 120)
    assert captured["auth"] == "Bearer fake-key"
    assert _FakeSession.last is not None and _FakeSession.last.closed is True


def test_chat_stream_context_cancel_closes_session(monkeypatch):
    cfg = _mock_cfg(real=True)
    client = Client(cfg)

    cancel_flag = {"v": False}
    ctx = SimpleNamespace(is_cancelled=lambda: cancel_flag["v"])

    closed_evt = threading.Event()

    class _SlowResp(_FakeStreamResponse):
        def iter_lines(self, decode_unicode: bool = False):
            # 慢慢吐：让 cancel 监视线程有机会触发 close
            for i in range(50):
                if closed_evt.is_set():
                    raise RuntimeError("session closed")
                time.sleep(0.02)
                yield f'data: {{"choices":[{{"delta":{{"content":"x{i}"}}}}]}}'

    class _SlowSession(_FakeSession):
        def __init__(self):
            super().__init__(_SlowResp([]))

        def close(self):
            super().close()
            closed_evt.set()

    monkeypatch.setattr("internal.llm.llm.requests.Session", _SlowSession)

    tokens: List[str] = []

    def _trigger_cancel():
        time.sleep(0.05)
        cancel_flag["v"] = True

    threading.Thread(target=_trigger_cancel, daemon=True).start()

    full = client.chat_stream_context(
        ctx=ctx,
        system_prompt="",
        messages=[Message(role="user", content="hi")],
        on_token=lambda t: tokens.append(t),
    )
    # cancel 后回退路径返回 "[已中断]"
    assert full == "[已中断]"
    assert closed_evt.is_set()

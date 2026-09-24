"""Task 24：fill_params_from_preference 5 键映射 + 不覆盖语义。"""
from types import SimpleNamespace

from internal.agent.agent import UnifiedAgent


def _stub_agent(prefs: dict):
    """直接走 UnifiedAgent._fill_params_from_preference 的最小可用 stub。

    UnifiedAgent.__init__ 副作用大（构造 LLM/RAG/PG），这里用 SimpleNamespace
    给 ``self.preference.get_all`` 即可，把方法当成 unbound 函数手动 bind。
    """
    pref = SimpleNamespace(get_all=lambda: dict(prefs))
    return SimpleNamespace(
        preference=pref,
        _PREFERENCE_PARAM_MAP=UnifiedAgent._PREFERENCE_PARAM_MAP,
    )


def _fill(prefs: dict, params: dict):
    agent = _stub_agent(prefs)
    UnifiedAgent._fill_params_from_preference(agent, params)
    return params


def test_fills_city_aliases():
    out = _fill({"城市": "北京"}, {})
    assert out == {"city": "北京", "location": "北京", "location_name": "北京"}


def test_fills_timezone_aliases():
    out = _fill({"时区": "Asia/Shanghai"}, {})
    assert out == {
        "timezone": "Asia/Shanghai",
        "tz": "Asia/Shanghai",
        "time_zone": "Asia/Shanghai",
    }


def test_fills_name_language_country():
    out = _fill(
        {"姓名": "张三", "语言": "zh-CN", "国家": "中国"},
        {},
    )
    assert out["name"] == "张三"
    assert out["username"] == "张三"
    assert out["user_name"] == "张三"
    assert out["language"] == "zh-CN"
    assert out["lang"] == "zh-CN"
    assert out["country"] == "中国"
    assert out["nation"] == "中国"


def test_does_not_overwrite_existing_non_empty():
    out = _fill({"城市": "北京"}, {"city": "上海"})
    # 已有非空 city 不被覆盖；其余 alias 仍按偏好补齐
    assert out["city"] == "上海"
    assert out["location"] == "北京"
    assert out["location_name"] == "北京"


def test_overwrites_empty_string_slot():
    out = _fill({"城市": "北京"}, {"city": ""})
    assert out["city"] == "北京"


def test_skips_when_preference_missing():
    out = _fill({}, {"city": ""})
    assert out == {"city": ""}


def test_skips_keys_not_in_map():
    """偏好里有非映射表的键时不应注入未声明的参数名。"""
    out = _fill({"爱好": "篮球"}, {})
    assert out == {}


def test_safe_when_get_all_raises():
    class _BadPref:
        def get_all(self):
            raise RuntimeError("oops")

    agent = SimpleNamespace(
        preference=_BadPref(),
        _PREFERENCE_PARAM_MAP=UnifiedAgent._PREFERENCE_PARAM_MAP,
    )
    params = {"city": ""}
    UnifiedAgent._fill_params_from_preference(agent, params)
    # 异常吞没，params 不应被破坏
    assert params == {"city": ""}


def test_non_dict_params_noop():
    agent = _stub_agent({"城市": "北京"})
    UnifiedAgent._fill_params_from_preference(agent, None)  # 不抛异常即可

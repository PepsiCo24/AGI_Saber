import threading
import time
from dataclasses import dataclass, field
from typing import Any, Callable, Dict, List, Optional

from internal.agent.subagents import SubAgentTask
from internal.graph.task_graph import NodeStatus, NodeType, TaskGraph
from internal.promptctx import StepObservation, ToolCallTrace


@dataclass
class GraphConfig:
    max_parallel: int = 2
    race_timeout_ms: int = 30000
    enable_racing: bool = True


@dataclass
class NodeResult:
    status: NodeStatus
    result: str = ""
    error: str = ""


@dataclass
class GraphResult:
    observations: List[str] = field(default_factory=list)
    node_results: Dict[str, NodeResult] = field(default_factory=dict)
    interrupted: bool = False
    interrupted_at: str = ""
    interrupted_msg: str = ""


class GraphRuntime:
    """按拓扑层级并行执行 TaskGraph，支持 race group 和取消。"""

    def __init__(
        self,
        graph: TaskGraph,
        agent,
        cfg: GraphConfig,
        tools: Dict[str, Any],
        task: Optional[dict] = None,
        on_event: Optional[Callable[[dict], None]] = None,
    ):
        if cfg.max_parallel <= 0:
            cfg.max_parallel = 2
        if cfg.race_timeout_ms <= 0:
            cfg.race_timeout_ms = 30000
        self.graph = graph
        self.agent = agent
        self.cfg = cfg
        self.tools = tools
        self.task = task if task is not None else {}
        self.on_event = on_event
        self._sem = threading.Semaphore(cfg.max_parallel)
        self._lock = threading.RLock()
        self._results: Dict[str, str] = {}
        self._errors: Dict[str, str] = {}

    def execute(self, token) -> GraphResult:
        try:
            levels = self.graph.topological_levels()
        except Exception as e:
            return GraphResult(interrupted_msg=f"图校验失败: {e}")

        for idx, level in enumerate(levels):
            if _is_cancelled(token):
                return self._build_interrupted(f"在第 {idx} 层执行前被中断")

            groups = self._group_by_race(level)
            threads = []
            for group_name, node_ids in groups:
                target = self._race_group if group_name and self.cfg.enable_racing else self._execute_group
                t = threading.Thread(target=target, args=(token, group_name, node_ids), daemon=True)
                threads.append(t)
                t.start()
            for t in threads:
                t.join()

            if _is_cancelled(token):
                return self._build_interrupted(f"在第 {idx} 层执行后被中断")
            self._save_snapshot()

        return self._build_result()

    def _execute_group(self, token, _group_name: str, node_ids: List[str]) -> None:
        threads = []
        for node_id in node_ids:
            t = threading.Thread(target=self._execute_node, args=(token, node_id), daemon=True)
            threads.append(t)
            t.start()
        for t in threads:
            t.join()

    def _race_group(self, token, group_name: str, node_ids: List[str]) -> None:
        done = threading.Event()
        results = []
        results_lock = threading.Lock()

        def runner(node_id: str) -> None:
            if done.is_set() or _is_cancelled(token):
                self.graph.set_node_status(node_id, NodeStatus.CANCELLED)
                return
            result, error = self._execute_single_node(token, node_id, record=False)
            with results_lock:
                results.append((node_id, result, error))
                if error is None and not done.is_set():
                    done.set()
                    self.graph.set_node_result(node_id, result)
                    self._record_success(node_id, result)

        threads = []
        for node_id in node_ids:
            t = threading.Thread(target=runner, args=(node_id,), daemon=True)
            threads.append(t)
            t.start()

        timeout = self.cfg.race_timeout_ms / 1000.0
        done.wait(timeout)
        for t in threads:
            t.join(0.01)

        winner = None
        with results_lock:
            for node_id, _result, error in results:
                if error is None:
                    winner = node_id
                    break
        if winner is not None:
            for node_id in node_ids:
                if node_id != winner and self.graph.nodes[node_id].status != NodeStatus.DONE:
                    self.graph.set_node_status(node_id, NodeStatus.SKIPPED)
            return

        last_error = "竞速组无成功结果"
        with results_lock:
            for _node_id, _result, error in results:
                if error:
                    last_error = error
        for node_id in node_ids:
            self.graph.set_node_error(node_id, last_error)
            self._record_failure(node_id, last_error)

    def _execute_node(self, token, node_id: str) -> None:
        result, error = self._execute_single_node(token, node_id, record=True)
        if error is not None:
            with self._lock:
                self._errors[node_id] = error
        else:
            with self._lock:
                self._results[node_id] = result

    def _execute_single_node(self, token, node_id: str, record: bool = True):
        with self._sem:
            node = self.graph.nodes[node_id]
            if _is_cancelled(token):
                self.graph.set_node_status(node_id, NodeStatus.CANCELLED)
                return "", "被用户中断"

            self.graph.set_node_status(node_id, NodeStatus.RUNNING)
            if node.type == NodeType.SUBAGENT:
                return self._execute_subagent_node(token, node_id, record)

            tool = self.tools.get(node.tool_name)
            if tool is None:
                error = f"工具 {node.tool_name} 不在允许列表中"
                self.graph.set_node_error(node_id, error)
                if record:
                    self._record_failure(node_id, error)
                return "", error

            max_retries = max(1, int(getattr(self.agent.cfg, "max_retries", 1)))
            retry_delay = float(getattr(self.agent.cfg, "retry_delay_ms", 0)) / 1000.0
            last_error = ""
            for attempt in range(max_retries):
                if _is_cancelled(token):
                    self.graph.set_node_status(node_id, NodeStatus.CANCELLED)
                    return "", "被用户中断"
                try:
                    result = _call_tool(tool, dict(node.params or {}))
                    self.graph.set_node_result(node_id, result)
                    if record:
                        self._record_success(node_id, result)
                    return result, None
                except Exception as e:
                    last_error = str(e)
                    self.graph.set_node_retry_count(node_id, attempt + 1)
                    if attempt < max_retries - 1 and retry_delay > 0:
                        time.sleep(retry_delay)

            self.graph.set_node_error(node_id, last_error)
            if record:
                self._record_failure(node_id, last_error)
            return "", last_error

    def _execute_subagent_node(self, token, node_id: str, record: bool = True):
        node = self.graph.nodes[node_id]
        registry = getattr(self.agent, "subagents", None)
        subagent = registry.get(node.tool_name) if registry is not None else None
        if subagent is None:
            error = f"子代理 {node.tool_name} 未注册"
            self.graph.set_node_error(node_id, error)
            if record:
                self._record_failure(node_id, error)
            return "", error

        upstream = {
            dep: self.graph.nodes[dep].result
            for dep in node.depends_on or []
            if dep in self.graph.nodes and self.graph.nodes[dep].result
        }
        task = SubAgentTask(
            id=node_id,
            goal=str((node.params or {}).get("goal") or node.name or ""),
            query=str(self.task.get("query") or self.task.get("message") or ""),
            upstream=upstream,
        )
        try:
            setattr(self.agent, "last_subagent_task", task)
        except Exception:
            pass

        max_retries = max(1, int(getattr(self.agent.cfg, "max_retries", 1)))
        retry_delay = float(getattr(self.agent.cfg, "retry_delay_ms", 0)) / 1000.0
        last_error = ""
        for attempt in range(max_retries):
            if _is_cancelled(token):
                self.graph.set_node_status(node_id, NodeStatus.CANCELLED)
                return "", "被用户中断"
            try:
                result = str(subagent.run(task))
                self.graph.set_node_result(node_id, result)
                if record:
                    self._record_success(node_id, result)
                return result, None
            except Exception as e:
                last_error = str(e)
                self.graph.set_node_retry_count(node_id, attempt + 1)
                if attempt < max_retries - 1 and retry_delay > 0:
                    time.sleep(retry_delay)

        self.graph.set_node_error(node_id, last_error)
        if record:
            self._record_failure(node_id, last_error)
        return "", last_error

    def _group_by_race(self, level: List[str]) -> List[tuple]:
        group_map: Dict[str, List[str]] = {}
        no_group: List[str] = []
        for node_id in level:
            group = self.graph.nodes[node_id].race_group
            if group:
                group_map.setdefault(group, []).append(node_id)
            else:
                no_group.append(node_id)
        groups = [(name, ids) for name, ids in sorted(group_map.items())]
        groups.extend(("", [node_id]) for node_id in no_group)
        return groups

    def _record_success(self, node_id: str, result: str) -> None:
        node = self.graph.nodes[node_id]
        self._push_task_mem(StepObservation(
            step_id=_node_step_id(node_id), tool_name=node.tool_name, result=result, success=True,
        ))
        self._record_tool_call(ToolCallTrace(tool_name=node.tool_name, success=True, summary=result))

    def _record_failure(self, node_id: str, error: str) -> None:
        node = self.graph.nodes[node_id]
        self._push_task_mem(StepObservation(
            step_id=_node_step_id(node_id), tool_name=node.tool_name, error=error, success=False,
        ))
        self._record_tool_call(ToolCallTrace(tool_name=node.tool_name, success=False, summary=error))

    def _push_task_mem(self, obs: StepObservation) -> None:
        fn = getattr(self.agent, "push_task_mem", None)
        if callable(fn):
            fn(obs)

    def _record_tool_call(self, trace: ToolCallTrace) -> None:
        fn = getattr(self.agent, "record_tool_call", None)
        if callable(fn):
            fn(trace)

    def _save_snapshot(self) -> None:
        fn = getattr(self.agent, "save_snapshot", None)
        if callable(fn):
            fn(self.task)

    def _build_result(self) -> GraphResult:
        node_results = {
            node_id: NodeResult(status=node.status, result=node.result, error=node.error)
            for node_id, node in self.graph.nodes.items()
        }
        return GraphResult(observations=self.graph.successful_results(), node_results=node_results)

    def _build_interrupted(self, msg: str) -> GraphResult:
        for node in self.graph.nodes.values():
            if node.status in {NodeStatus.PENDING, NodeStatus.RUNNING}:
                node.status = NodeStatus.CANCELLED
        result = self._build_result()
        result.interrupted = True
        result.interrupted_msg = msg
        return result


def _call_tool(tool, params: Dict[str, Any]) -> str:
    if hasattr(tool, "func"):
        return str(tool.func(params))
    execute = getattr(tool, "execute", None)
    if callable(execute):
        return str(execute(params))
    raise RuntimeError("tool has no callable func/execute")


def _is_cancelled(token) -> bool:
    return bool(token is not None and callable(getattr(token, "is_cancelled", None)) and token.is_cancelled())


def _node_step_id(node_id: str) -> int:
    if node_id.startswith("n"):
        try:
            return int(node_id[1:])
        except ValueError:
            return 0
    return 0

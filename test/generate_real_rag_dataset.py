#!/usr/bin/env python3
"""Crawl public Juejin posts and build the retrieval benchmark JSONL files.

Output:
  test.txt: 10,000 real article chunks from 1,250 public Juejin posts.
  gold.txt: 50 queries, each with five relevant chunk IDs.

The construction follows test/测试rag要求.md: chunk size 200, overlap 50,
10,035 candidate chunks followed by deterministic deletion of 35 non-gold
chunks. Every output record retains source provenance and a content hash.
"""

from __future__ import annotations

import concurrent.futures
import datetime as dt
import hashlib
import html
from html.parser import HTMLParser
import json
import os
from pathlib import Path
import random
import re
import sys
import threading
import time
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen


ROOT = Path(__file__).resolve().parent
TEST_PATH = ROOT / "test.txt"
GOLD_PATH = ROOT / "gold.txt"

SITEMAP_URL = "https://juejin.cn/sitemap/posts/index1.xml"
POST_URL = "https://juejin.cn/post/{article_id}"
USER_AGENT = (
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "
    "AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0 Safari/537.36 "
    "AGI-saber-RAG-benchmark/1.0"
)

CHUNK_SIZE = 200
CHUNK_OVERLAP = 50
TOTAL_DOCS = 1_250
CANDIDATE_CHUNKS = 10_035
DELETE_COUNT = 35
FINAL_CHUNKS = 10_000
MAX_WORKERS = 6
SITEMAP_SAMPLE_SIZE = 3_000
FETCH_BATCH_SIZE = 200
KNOWN_CATEGORIES = {
    "后端",
    "前端",
    "Android",
    "iOS",
    "人工智能",
    "开发工具",
    "代码人生",
    "阅读",
}

# Hand-written after reviewing the 50 selected source articles. The fallback in
# make_question keeps the crawler usable if the live sitemap changes later.
MANUAL_QUESTIONS = {
    "7663014213996609582": "如何调用远程 MCP，让 Agent 完成酒店查询并自动打开浏览器？实现链路和注意事项是什么？",
    "7662777075909312563": "Preact 的 Hydration 是什么，它如何复用服务端渲染结果并恢复客户端交互？",
    "7662624197684756480": "SmartMediaKit 如何实现低延迟播放、推流、转发和国标接入，工程集成时要注意什么？",
    "7662624197682446388": "CoStrict 入选工信部人工智能应用典型案例体现了哪些 AI 编程能力和落地场景？",
    "7662319290114703400": "情感可控语音合成中，解耦派与涌现派分别怎样实现控制，IndexTTS 为什么做出相应选择？",
    "7662267147439554598": "把老项目从 Java 21 升级到 Java 26 会遇到哪些兼容性问题，应如何排查和处理？",
    "7661971304590721075": "rustzen-admin 如何从基础后台演进为可部署的工程模板，其架构和交付能力有哪些变化？",
    "7661830907991048232": "对 iOS 版麦当劳应用进行逆向分析时，完整流程、关键工具和判断依据是什么？",
    "7661505897453142051": "TokenFaucet 解决什么问题，它的 API 使用方式、核心流程和限制是什么？",
    "7660902254527217704": "为什么 Redis 的 KEYS 命令可能拖垮线上实例，如何用 SCAN 安全地完成遍历？",
    "7660850473310978111": "Java 线程池的核心参数如何协同工作，线上应该怎样估算、配置并监控它们？",
    "7660788416040943656": "如何使用 xxd 查看和分析 DICOM（dcm）文件，输出中的关键字节应怎样理解？",
    "7660707431754711046": "湖库一体架构解决了哪些传统数据平台问题，它在 2026 年的优势、代价和适用边界是什么？",
    "7660683463603781674": "Go 泛型的真正价值体现在哪里，怎样在保证类型安全的同时复用算法和数据结构？",
    "7660329592071634959": "CryptoHack XOR 题如何利用异或的四个数学性质逐步还原 Flag？",
    "7660079523233726506": "选择 IT 自动化运维平台时应比较哪些核心能力，四类主流方案各适合什么场景？",
    "7660053470330650674": "setuptools 72 删除 test 命令后为什么会触发 ModuleNotFoundError，受影响的包应如何修复？",
    "7659750807068835855": "为什么只把 AI 当高级搜索框无法改善 ITSM，真正有效的系统集成需要打通哪些环节？",
    "7659671273130147890": "Rust 图像处理中如何用齐次坐标和共享仿射变换实现缩放与斜切畸变？",
    "7659569475197632575": "AI 加速代码产出后，合并流程为什么需要增加质量闸门，闸门应检查哪些风险？",
    "7659249446257262607": "与 telnet 相比，nc 在网络连通性和端口调试中有哪些能力，典型命令怎样使用？",
    "7658944654235697188": "Linux 内存 zone 中的水位线、free_area、buddy 系统和页面回收入口如何协同工作？",
    "7658428809248063538": "如何手写一个支持文件读写的 MCP Server，让大模型安全访问本地文件系统？",
    "7658522877016342578": "Spring Boot 3 项目中如何从零设计通用持久层 CRUD 架构并搭建可复用脚手架？",
    "7658185561329090560": "这篇文章记录了哪些百度网盘使用场景、实际问题和处理方法？",
    "7658132119805935656": "给中文文章自动配插图的 Skill 如何工作，它为什么能快速获得大量 GitHub Star？",
    "7657794899358973986": "如何在 IntelliJ IDEA 中顺畅使用 Claude Code，安装、配置和日常工作流分别是什么？",
    "7657743769991348265": "安装 VMware 时出现 Windows 蓝屏通常有哪些原因，应按什么顺序定位和解决？",
    "7657477469918773289": "Java 正则表达式怎样完成文本替换，分组、匹配和替换字符串有哪些易错点？",
    "7657357573112528915": "安卓或苹果手机中的音乐如何传到电脑，五种方法的步骤和适用条件分别是什么？",
    "7657098993717559305": "给 AI 生成 SQL 增加哪三类规则可以降低翻车率，规则为什么有效？",
    "7656707220898857000": "如何通过 CC Switch 为 DeepSeek 开启思考模式，完整配置步骤和验证方法是什么？",
    "7656841584964681762": "vLLM looper 在 GPQA-Diamond 获得 96.0 分的方案做了什么，结果应如何解读？",
    "7656023099708276745": "由多个 Codex Agent 组成的 AI 模拟盘团队如何分工、执行纪律并控制交易风险？",
    "7656077196734267418": "React 应用如何实现路由守卫和权限控制，并正确处理未登录或无权限访问？",
    "7655891892416561171": "如何用 C# 实现西门子 S7 协议通信并搭建可靠的 PLC 工业数据采集程序？",
    "7655521075666026505": "Spring Boot、Vue 和 FFmpeg 如何配合实现视频拉流播放，前后端数据链路是什么？",
    "7655378241421033499": "为什么 AI Coding 能完成交付不等于开发者真正掌握了知识，应该怎样继续学习？",
    "7654898515792216098": "为什么说 Rust 是声明式而不是手动内存管理，所有权系统如何表达资源生命周期？",
    "7655127804790571058": "算法工程师在开发和落地算法时面临哪些主要痛点，工程流程可以怎样改善？",
    "7654830404369154058": "Struts2 如何实现页面模板化，模板复用的配置和渲染流程是什么？",
    "7654804923790213163": "前端 AI 对话界面如何实时统计字数和 Token，并兼顾性能与不同模型的计数差异？",
    "7654561747157106730": "FreeSWITCH 的 SIP REGISTER 为什么会超时或返回 403，背后的三个机制是什么？",
    "7654371662875443234": "使用 @solana/web3.js 连接 Solana 钱包时常见的三个坑是什么，分别如何修复？",
    "7654145152102449158": "HarmonyOS 收音记录应用如何设计和实现，录音、存储与界面流程有哪些关键点？",
    "7654119725073449010": "从大规模 Token 使用数据看，Prompt Caching 应如何设计缓存键、命中策略和成本控制？",
    "7653373167660367918": "AI 编程普及后究竟是谁在写代码，开发者的判断、责任和工作方式发生了什么变化？",
    "7653410900831371291": "从 CPU 空闲问题出发，多线程和并发机制为什么会出现，它们解决了什么调度矛盾？",
    "7653035007564431375": "GLM-5.2 与 DeepSeek V4 Pro 的幻觉率对比是怎样测出的，相关结果应如何审慎解读？",
    "7652644062928683023": "如何在线性时间内删除链表的倒数第 N 个结点，双指针解法为什么正确？",
}

_print_lock = threading.Lock()


def log(message: str) -> None:
    with _print_lock:
        print(message, flush=True)


def request_bytes(
    url: str,
    *,
    payload: dict[str, Any] | None = None,
    timeout: int = 30,
    attempts: int = 5,
) -> bytes:
    data = None
    headers = {
        "User-Agent": USER_AGENT,
        "Accept": "text/html,application/json;q=0.9,*/*;q=0.8",
        "Accept-Language": "zh-CN,zh;q=0.9,en;q=0.7",
    }
    if payload is not None:
        data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        headers["Content-Type"] = "application/json"
    last_error: Exception | None = None
    for attempt in range(attempts):
        try:
            req = Request(url, data=data, headers=headers, method="POST" if data else "GET")
            with urlopen(req, timeout=timeout) as response:
                return response.read()
        except (HTTPError, URLError, TimeoutError, OSError) as exc:
            last_error = exc
            if isinstance(exc, HTTPError) and exc.code not in {408, 429, 500, 502, 503, 504}:
                break
            time.sleep(min(8.0, 0.6 * (2**attempt)))
    raise RuntimeError(f"request failed after {attempts} attempts: {url}: {last_error}")


def fetch_sitemap_candidates() -> list[dict[str, Any]]:
    """Sample public post URLs from the official sitemap, deterministically."""
    sitemap = request_bytes(SITEMAP_URL).decode("utf-8", errors="replace")
    matches = re.findall(
        r"<url>\s*<loc>(https://juejin\.cn/post/(\d+))</loc>\s*<lastmod>([^<]+)</lastmod>",
        sitemap,
    )
    if len(matches) < SITEMAP_SAMPLE_SIZE:
        raise RuntimeError(f"sitemap only contained {len(matches)} post URLs")
    rng = random.Random(20260717)
    positions = sorted(rng.sample(range(len(matches)), SITEMAP_SAMPLE_SIZE))
    candidates = [
        {
            "url": matches[position][0],
            "article_id": matches[position][1],
            "sitemap_lastmod": matches[position][2],
        }
        for position in positions
    ]
    log(f"Sitemap URLs: {len(matches)}; deterministic sample: {len(candidates)}")
    return candidates


class ArticleBodyParser(HTMLParser):
    BLOCKS = {"p", "div", "h1", "h2", "h3", "h4", "h5", "h6", "li", "pre", "blockquote", "table", "tr", "ul", "ol", "br"}
    SKIP = {"script", "style", "svg", "noscript"}

    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.depth = 0
        self.skip_depth = 0
        self.parts: list[str] = []

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        attr_map = dict(attrs)
        classes = (attr_map.get("class") or "").split()
        if self.depth == 0 and tag == "div" and "article-viewer" in classes:
            self.depth = 1
            return
        if not self.depth:
            return
        self.depth += 1
        if tag in self.SKIP:
            self.skip_depth += 1
        elif tag in self.BLOCKS and not self.skip_depth:
            self.parts.append("\n")

    def handle_endtag(self, tag: str) -> None:
        if not self.depth:
            return
        if tag in self.SKIP and self.skip_depth:
            self.skip_depth -= 1
        elif tag in self.BLOCKS and not self.skip_depth:
            self.parts.append("\n")
        self.depth -= 1

    def handle_data(self, data: str) -> None:
        if self.depth and not self.skip_depth:
            self.parts.append(data)

    def text(self) -> str:
        return normalize_text("".join(self.parts))


def clean_inline(value: Any) -> str:
    return re.sub(r"\s+", " ", html.unescape(str(value))).strip()


def normalize_text(value: str) -> str:
    value = html.unescape(value).replace("\r\n", "\n").replace("\r", "\n")
    value = value.replace("\x00", "").replace("\u00ad", "")
    value = re.sub(r"[ \t\f\v]+", " ", value)
    lines = [line.strip() for line in value.splitlines()]
    value = "\n".join(line for line in lines if line)
    value = re.sub(r"\n{2,}", "\n", value)
    return value.strip()


def iso_time(value: Any) -> str:
    try:
        return dt.datetime.fromtimestamp(int(value), tz=dt.timezone.utc).isoformat().replace("+00:00", "Z")
    except (TypeError, ValueError, OSError):
        return ""


def split_chunks(text: str) -> list[str]:
    """Unicode-safe 200-character windows with the required 50-char overlap."""
    text = normalize_text(text)
    if not text:
        return []
    chunks: list[str] = []
    step = CHUNK_SIZE - CHUNK_OVERLAP
    for start in range(0, len(text), step):
        chunk = text[start : start + CHUNK_SIZE].strip()
        if len(chunk) >= 80:
            chunks.append(chunk)
        if start + CHUNK_SIZE >= len(text):
            break
    return chunks


def first_meta(page: str, itemprop: str) -> str:
    pattern = rf'<meta\s+itemprop="{re.escape(itemprop)}"\s+content="([^"]*)"'
    match = re.search(pattern, page)
    return clean_inline(match.group(1)) if match else ""


def article_metadata(page: str, candidate: dict[str, Any]) -> dict[str, Any] | None:
    title = first_meta(page, "headline")
    published_at = first_meta(page, "datePublished")
    keyword_text = first_meta(page, "keywords")
    author_match = re.search(
        r'<div\s+itemprop="author"[^>]*>\s*<meta\s+itemprop="name"\s+content="([^"]*)"',
        page,
    )
    author = clean_inline(author_match.group(1)) if author_match else ""
    keywords = [clean_inline(value) for value in keyword_text.split(",") if clean_inline(value)]
    category = next((value for value in reversed(keywords) if value in KNOWN_CATEGORIES), "其他技术")
    tags = [value for value in keywords if value != category]
    if not title or not author:
        return None
    return {
        "article_id": candidate["article_id"],
        "title": title,
        "author": author,
        "category": category,
        "tags": tags,
        "published_at": published_at,
        "updated_at": candidate["sitemap_lastmod"],
        "url": candidate["url"],
    }


def fetch_article(candidate: dict[str, Any]) -> dict[str, Any] | None:
    try:
        raw = request_bytes(candidate["url"])
        page = raw.decode("utf-8", errors="replace")
        metadata = article_metadata(page, candidate)
        if metadata is None:
            return None
        parser = ArticleBodyParser()
        parser.feed(page)
        body = parser.text()
        chunks = split_chunks(body)
        if len(chunks) < 9:
            return None
        result = metadata
        result["body_chars"] = len(body)
        result["all_chunks"] = chunks
        return result
    except Exception as exc:
        log(f"[skip] {candidate['article_id']}: {exc}")
        return None


def fetch_documents() -> list[dict[str, Any]]:
    candidates = fetch_sitemap_candidates()
    documents: list[dict[str, Any]] = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=MAX_WORKERS) as pool:
        for start in range(0, len(candidates), FETCH_BATCH_SIZE):
            batch = candidates[start : start + FETCH_BATCH_SIZE]
            # executor.map preserves sitemap sample order, unlike completion order.
            for document in pool.map(fetch_article, batch):
                if document is not None:
                    documents.append(document)
            log(f"Fetched {min(start + len(batch), len(candidates))}/{len(candidates)}; valid {len(documents)}/{TOTAL_DOCS}")
            if len(documents) >= TOTAL_DOCS:
                return documents[:TOTAL_DOCS]
    raise RuntimeError(f"only {len(documents)} valid documents from sitemap sample")


def evenly_spaced_indices(length: int, count: int) -> list[int]:
    if count <= 0 or length < count:
        raise ValueError(f"cannot choose {count} positions from length {length}")
    if count == 1:
        return [0]
    raw = [round(i * (length - 1) / (count - 1)) for i in range(count)]
    # Rounding can theoretically collide; fill deterministically if it does.
    selected: list[int] = []
    for value in raw:
        if value not in selected:
            selected.append(value)
    for value in range(length):
        if len(selected) >= count:
            break
        if value not in selected:
            selected.append(value)
    return sorted(selected[:count])


def choose_gold_documents(documents: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Spread the 50 gold documents through the complete sitemap sample."""
    positions = evenly_spaced_indices(len(documents), 50)
    return [documents[position] for position in positions]


def make_question(document: dict[str, Any]) -> str:
    manual = MANUAL_QUESTIONS.get(document["article_id"])
    if manual:
        return manual
    tags = "、".join(document["tags"][:3])
    tag_clause = f"（涉及{tags}）" if tags else ""
    return f"文章《{document['title']}》{tag_clause}主要解决什么问题？请说明文中的核心思路、实现过程和关键注意事项。"


def build_outputs(documents: list[dict[str, Any]]) -> tuple[list[dict[str, Any]], list[dict[str, Any]], list[str]]:
    if len(documents) != TOTAL_DOCS:
        raise RuntimeError(f"expected {TOTAL_DOCS} documents, got {len(documents)}")

    gold_documents = choose_gold_documents(documents)
    gold_ids = {document["article_id"] for document in gold_documents}
    distractors = [document for document in documents if document["article_id"] not in gold_ids]
    if len(gold_documents) != 50 or len(distractors) != 1_200:
        raise RuntimeError("gold/distractor document allocation mismatch")

    chosen: dict[str, list[int]] = {}
    for document in gold_documents:
        chosen[document["article_id"]] = evenly_spaced_indices(len(document["all_chunks"]), 5)
    for index, document in enumerate(distractors):
        count = 9 if index < 185 else 8
        chosen[document["article_id"]] = evenly_spaced_indices(len(document["all_chunks"]), count)

    if sum(len(indices) for indices in chosen.values()) != CANDIDATE_CHUNKS:
        raise RuntimeError("candidate chunk count mismatch")

    # Delete one non-gold chunk from each of the first 35 nine-chunk documents.
    deleted_chunk_ids: list[str] = []
    for document in distractors[:DELETE_COUNT]:
        source_index = chosen[document["article_id"]].pop()
        deleted_chunk_ids.append(chunk_id(document["article_id"], source_index))

    records: list[dict[str, Any]] = []
    record_by_document: dict[str, list[dict[str, Any]]] = {}
    fetched_at = dt.datetime.now(tz=dt.timezone.utc).isoformat().replace("+00:00", "Z")
    for document in documents:
        for source_index in chosen[document["article_id"]]:
            content = document["all_chunks"][source_index]
            record = {
                "chunk_id": chunk_id(document["article_id"], source_index),
                "document_id": document["article_id"],
                "chunk_index": source_index,
                "content": content,
                "title": document["title"],
                "author": document["author"],
                "category": document["category"],
                "tags": document["tags"],
                "url": document["url"],
                "published_at": document["published_at"],
                "updated_at": document["updated_at"],
                "fetched_at": fetched_at,
                "content_sha256": hashlib.sha256(content.encode("utf-8")).hexdigest(),
            }
            records.append(record)
            record_by_document.setdefault(document["article_id"], []).append(record)

    records.sort(key=lambda record: (record["document_id"], record["chunk_index"]))
    gold: list[dict[str, Any]] = []
    for query_index, document in enumerate(gold_documents, 1):
        relevant = record_by_document[document["article_id"]]
        relevant.sort(key=lambda record: record["chunk_index"])
        relevant_ids = [record["chunk_id"] for record in relevant]
        gold.append(
            {
                "query_id": f"q{query_index:03d}",
                "question": make_question(document),
                "ground_truth": relevant_ids,
                "relevance": {chunk: (3 if rank == 0 else 2 if rank < 3 else 1) for rank, chunk in enumerate(relevant_ids)},
                "source_article_id": document["article_id"],
                "source_url": document["url"],
                "category": document["category"],
            }
        )
    return records, gold, deleted_chunk_ids


def chunk_id(article_id: str, source_index: int) -> str:
    return f"juejin_{article_id}_{source_index:05d}"


def write_jsonl(path: Path, records: list[dict[str, Any]]) -> None:
    temp_path = path.with_suffix(path.suffix + ".tmp")
    with temp_path.open("w", encoding="utf-8", newline="\n") as handle:
        for record in records:
            handle.write(json.dumps(record, ensure_ascii=False, separators=(",", ":")) + "\n")
    os.replace(temp_path, path)


def validate(records: list[dict[str, Any]], gold: list[dict[str, Any]], deleted: list[str]) -> None:
    if len(records) != FINAL_CHUNKS:
        raise RuntimeError(f"expected {FINAL_CHUNKS} final chunks, got {len(records)}")
    if len(gold) != 50:
        raise RuntimeError(f"expected 50 gold queries, got {len(gold)}")
    ids = [record["chunk_id"] for record in records]
    if len(ids) != len(set(ids)):
        raise RuntimeError("duplicate chunk IDs")
    # Natural crawled corpora can contain reposts, shared code, and repeated
    # boilerplate. Preserve those real duplicates; chunk IDs remain unique.
    id_set = set(ids)
    used: set[str] = set()
    for item in gold:
        truth = item["ground_truth"]
        if len(truth) != 5 or len(set(truth)) != 5:
            raise RuntimeError(f"invalid ground truth for {item['query_id']}")
        if not set(truth) <= id_set:
            raise RuntimeError(f"missing ground truth chunk for {item['query_id']}")
        if used.intersection(truth):
            raise RuntimeError(f"ground truth reused by {item['query_id']}")
        used.update(truth)
    if len(deleted) != DELETE_COUNT or id_set.intersection(deleted):
        raise RuntimeError("deterministic deletion check failed")
    if len({record["document_id"] for record in records}) != TOTAL_DOCS:
        raise RuntimeError("document count mismatch")


def main() -> None:
    random.seed(20260717)
    log("Starting Juejin crawl (public /post pages only).")
    documents = fetch_documents()
    records, gold, deleted = build_outputs(documents)
    validate(records, gold, deleted)
    write_jsonl(TEST_PATH, records)
    write_jsonl(GOLD_PATH, gold)
    log(f"Wrote {TEST_PATH}: {len(records)} chunks from {len(documents)} documents")
    log(f"Wrote {GOLD_PATH}: {len(gold)} queries x 5 relevant chunks")
    log(f"Candidate chunks: {CANDIDATE_CHUNKS}; deleted: {len(deleted)}; final: {len(records)}")


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        log("Interrupted.")
        sys.exit(130)

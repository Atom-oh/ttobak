"""Render validated structured news; raw article bodies are never an input."""

import html
import re
from urllib.parse import quote, urlsplit

from news_pipeline import canonicalize_url

MAX_MARKDOWN_BYTES = 64000


def _text(value):
    text = html.escape(" ".join(str(value).split()), quote=False)
    return re.sub(r"([\\`*_{}\[\]()#+.!|~>-])", r"\\\1", text)


def _bullets(values):
    return "\n".join("- " + _text(value) for value in values)


def render_news_markdown(artifact, candidate, source_count):
    if type(source_count) is not int or not 1 <= source_count <= 3:
        raise ValueError("invalid source count")
    sections = [
        "# " + _text(artifact["title"]),
        f"**출처:** {source_count}건 ({_text(candidate.get('sourceName', ''))})",
        "## 핵심 요약\n\n" + _text(artifact["executiveSummary"]),
        "## 주요 사실\n\n" + "\n".join(
            f"- {_text(fact['fact'])} " + " ".join(f"[{_text(eid)}]" for eid in fact["evidenceIds"])
            for fact in artifact["keyFacts"]),
        "## 배경과 맥락\n\n" + _text(artifact["context"]),
        "## 영향\n\n### 사업\n\n" + _bullets(artifact["impact"]["business"])
        + "\n\n### 기술\n\n" + _bullets(artifact["impact"]["technical"]),
        "## 리스크와 기회\n\n### 리스크\n\n" + _bullets(artifact["risks"])
        + "\n\n### 기회\n\n" + _bullets(artifact["opportunities"]),
        "## AWS 관련성\n\n" + (_bullets(
            f"{item['service']}: {item['rationale']}" for item in artifact["awsRelevance"])
            or "AWS 관련성이 확인되지 않았습니다."),
        "## 다음 미팅 질문\n\n" + _bullets(artifact["meetingQuestions"]),
        "## 권장 액션\n\n" + _bullets(artifact["recommendedActions"]),
    ]
    evidence = []
    for item in artifact["evidence"]:
        # Validate identity without sorting/removing query parameters. Match
        # publisher transport component escaping and keep IPv6 authority brackets.
        original = urlsplit(item["sourceUrl"])
        authority = urlsplit(canonicalize_url(item["sourceUrl"])).netloc
        url = original._replace(
            netloc=authority,
            path=quote(original.path or "/", safe="/%:@!$&'()*+,;=-._~"),
            query=quote(original.query, safe="/?%:@!$&'()*+,;=-._~"),
            fragment=quote(original.fragment, safe="/?%:@!$&'()*+,;=-._~"),
        ).geturl()
        # Markdown decodes character references inside destinations. Escape
        # ampersands once so a literal query such as &copy; stays literal.
        url = url.replace("&", "&amp;")
        evidence.append(f"- [{_text(item['id'])}] {_text(item['supportingExcerpt'])}"
                        f" — [원문](<{url}>)")
    sections.append("## 근거 및 출처\n\n" + "\n".join(evidence))
    if artifact["tags"]:
        sections.append("**태그:** " + ", ".join(_text(tag) for tag in artifact["tags"]))
    result = "\n\n".join(sections) + "\n"
    if len(result.encode("utf-8")) > MAX_MARKDOWN_BYTES:
        raise ValueError("rendered news exceeds byte limit")
    return result

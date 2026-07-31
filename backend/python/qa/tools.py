"""Tool definitions and executor for Bedrock Converse API agentic loop."""

import logging
import re

from aws_docs import search_aws_docs, get_aws_recommendation
from web_search import gateway_web_search, format_web_results

logger = logging.getLogger(__name__)

TOOL_DEFINITIONS = [
    {
        "toolSpec": {
            "name": "search_knowledge_base",
            "description": "Search the Ttobak knowledge base for relevant documents about meetings, AWS, or uploaded files.",
            "inputSchema": {
                "json": {
                    "type": "object",
                    "properties": {
                        "query": {"type": "string", "description": "Search query"},
                        "numberOfResults": {"type": "integer", "description": "Number of results (1-10)", "default": 5}
                    },
                    "required": ["query"]
                }
            }
        }
    },
    {
        "toolSpec": {
            "name": "search_aws_docs",
            "description": "Search AWS official documentation for service details, best practices, and guides.",
            "inputSchema": {
                "json": {
                    "type": "object",
                    "properties": {
                        "query": {"type": "string", "description": "AWS documentation search query"},
                        "limit": {"type": "integer", "description": "Max results (1-5)", "default": 3}
                    },
                    "required": ["query"]
                }
            }
        }
    },
    {
        "toolSpec": {
            "name": "search_transcript",
            "description": "Search meeting transcript for specific topics or keywords. Use when the user asks about what was discussed.",
            "inputSchema": {
                "json": {
                    "type": "object",
                    "properties": {
                        "keywords": {"type": "string", "description": "Keywords to search for in transcript"}
                    },
                    "required": ["keywords"]
                }
            }
        }
    },
    {
        "toolSpec": {
            "name": "get_aws_recommendation",
            "description": "Get AWS service recommendations for a specific use case or architecture question.",
            "inputSchema": {
                "json": {
                    "type": "object",
                    "properties": {
                        "useCase": {"type": "string", "description": "Description of the use case or architecture question"}
                    },
                    "required": ["useCase"]
                }
            }
        }
    },
    {
        "toolSpec": {
            "name": "list_meetings",
            "description": "사용자의 미팅 목록을 검색합니다. 본인 미팅과 공유받은 미팅 모두 포함. 날짜, 태그, 키워드로 필터링 가능합니다.",
            "inputSchema": {
                "json": {
                    "type": "object",
                    "properties": {
                        "dateFrom": {"type": "string", "description": "시작 날짜 (ISO 8601, 예: 2026-04-01)"},
                        "dateTo": {"type": "string", "description": "종료 날짜 (ISO 8601, 예: 2026-04-22)"},
                        "tag": {"type": "string", "description": "태그 필터 (예: eks, database)"},
                        "keyword": {"type": "string", "description": "제목 키워드 검색"},
                        "limit": {"type": "integer", "description": "최대 결과 수 (기본 20)"}
                    }
                }
            }
        }
    },
    {
        "toolSpec": {
            "name": "get_meeting_detail",
            "description": "특정 미팅의 상세 내용(AI 요약, 트랜스크립트)을 가져옵니다. list_meetings에서 얻은 meetingId를 사용하세요. 미팅 내용에 대해 질문받았을 때 반드시 이 도구를 사용하세요.",
            "inputSchema": {
                "json": {
                    "type": "object",
                    "properties": {
                        "meetingId": {"type": "string", "description": "미팅 ID (list_meetings 결과에서 확인)"}
                    },
                    "required": ["meetingId"]
                }
            }
        }
    },
    {
        "toolSpec": {
            "name": "start_research",
            "description": "사용자가 특정 주제에 대해 심층 리서치를 요청할 때 사용합니다. Deep Research를 시작하고 결과 페이지 링크를 반환합니다.",
            "inputSchema": {
                "json": {
                    "type": "object",
                    "properties": {
                        "topic": {"type": "string", "description": "리서치 주제 (구체적일수록 좋은 결과)"},
                        "mode": {"type": "string", "description": "리서치 모드: quick (빠른 요약), standard (기본), deep (심층 분석)", "enum": ["quick", "standard", "deep"], "default": "standard"}
                    },
                    "required": ["topic"]
                }
            }
        }
    },
    {
        "toolSpec": {
            "name": "list_accounts",
            "description": "내가 속한 고객사(Account) 목록과 내 역할을 조회합니다. 특정 고객사의 인사이트/미팅을 묻기 전에 어떤 계정이 있는지 확인할 때 사용.",
            "inputSchema": {"json": {"type": "object", "properties": {}}}
        }
    },
    {
        "toolSpec": {
            "name": "get_account_insights",
            "description": "특정 고객사(Account)에 누적된 필드 인사이트를 기간/유형으로 조회합니다. SIFT(월간 인사이트), 2by2(리스크/기회), 어카운트 동향 정리에 사용. account는 고객사 이름/별칭으로 지정(예: '하나은행').",
            "inputSchema": {"json": {"type": "object", "properties": {
                "account": {"type": "string", "description": "고객사 이름 또는 별칭 (예: 하나은행)"},
                "from": {"type": "string", "description": "시작 시각 RFC3339 (예: 2026-05-01T00:00:00Z). 선택"},
                "to": {"type": "string", "description": "종료 시각 RFC3339 (예: 2026-05-31T23:59:59Z). 선택"},
                "types": {"type": "array", "items": {"type": "string"}, "description": "인사이트 유형 필터. 가능: trend, need, competitive, risk, opportunity, tech, stakeholder, action. 선택"}
            }, "required": ["account"]}}
        }
    },
    {
        "toolSpec": {
            "name": "get_account_brief",
            "description": "특정 고객사(Account)의 한눈 브리프 — 메타 + 유형별 인사이트 + 공유 미팅 + 연결된 리서치 목록을 한 번에. Player Card/분기 리뷰 준비, 어카운트 전반 파악에 사용. account는 고객사 이름/별칭.",
            "inputSchema": {"json": {"type": "object", "properties": {
                "account": {"type": "string", "description": "고객사 이름 또는 별칭 (예: 하나은행)"}
            }, "required": ["account"]}}
        }
    },
    {
        "toolSpec": {
            "name": "search_web",
            "description": "웹에서 최신 정보를 검색합니다. 최신 뉴스, 제품/서비스 출시 소식, 시세·가격, 경쟁사 동향, AWS 외 일반 주제 등 KB나 AWS 문서에 없을 정보에 사용하세요. 쿼리는 외부 검색 제공자로 전송되므로 일반화된 기술 키워드로만 구성할 것 — 고객사/참석자 실명, 내부 프로젝트 코드명, 회의에서 나온 구체적 금액·수치를 쿼리에 넣지 마세요.",
            "inputSchema": {
                "json": {
                    "type": "object",
                    "properties": {
                        "query": {"type": "string", "description": "검색 쿼리 (구체적일수록 좋음)"},
                        "maxResults": {"type": "integer", "description": "최대 결과 수 (1-10)", "default": 5}
                    },
                    "required": ["query"]
                }
            }
        }
    }
]


def execute_tool(tool_name, tool_input, context):
    """Execute a tool and return (result_text, source_uris).

    Returns a tuple so the caller can collect sources without re-executing the search.
    """
    try:
        if tool_name == "search_web":
            # Clamp to [1, 10]: a model-supplied 0/negative value would slice
            # to [] with error=None, which reads as a genuine "no results" —
            # exactly the failure/no-results ambiguity format_web_results
            # exists to prevent.
            try:
                max_results = int(tool_input.get("maxResults", 5))
            except (TypeError, ValueError):
                max_results = 5
            results, error = gateway_web_search(
                tool_input["query"],
                max(1, min(max_results, 10)),
            )
            return format_web_results(results, error)
        elif tool_name == "search_knowledge_base":
            results = context["retrieve_from_kb"](
                tool_input["query"],
                tool_input.get("numberOfResults", 5),
            )
            sources = [r["uri"] for r in results if r.get("uri")]
            return format_kb_results(results), sources
        elif tool_name == "search_aws_docs":
            results = search_aws_docs(
                tool_input["query"],
                tool_input.get("limit", 3),
            )
            sources = [d["url"] for d in results if d.get("url")]
            return format_docs_results(results), sources
        elif tool_name == "search_transcript":
            return search_in_transcript(
                tool_input["keywords"],
                context.get("transcript", ""),
            ), []
        elif tool_name == "get_aws_recommendation":
            return get_aws_recommendation(tool_input["useCase"]), []
        elif tool_name == "list_meetings":
            user_id = context.get("user_id")
            if not user_id:
                return "사용자 인증 정보가 없습니다.", []
            meetings = context["list_meetings"](
                user_id,
                date_from=tool_input.get("dateFrom"),
                date_to=tool_input.get("dateTo"),
                tag=tool_input.get("tag"),
                keyword=tool_input.get("keyword"),
                limit=tool_input.get("limit"),
            )
            return format_meetings_results(meetings), []
        elif tool_name == "get_meeting_detail":
            user_id = context.get("user_id")
            if not user_id:
                return "사용자 인증 정보가 없습니다.", []
            meeting_id = tool_input.get("meetingId", "")
            load_fn = context.get("load_meeting_context")
            if not load_fn:
                return "미팅 상세 조회 기능을 사용할 수 없습니다.", []
            content, err = load_fn(user_id, meeting_id)
            if err:
                return f"미팅 조회 실패: {err.get('message', 'unknown error')}", []
            if not content:
                return "미팅 내용이 비어있습니다.", []
            max_len = 6000
            if len(content) > max_len:
                content = content[:max_len] + f"\n\n... (총 {len(content)}자 중 {max_len}자까지 표시)"
            return content, []
        elif tool_name == "start_research":
            user_id = context.get("user_id")
            if not user_id:
                return "사용자 인증 정보가 없습니다.", []
            create_fn = context.get("create_research")
            if not create_fn:
                return "리서치 기능을 사용할 수 없습니다.", []
            check_fn = context.get("check_research_limit")
            if check_fn and not check_fn(user_id):
                return "일일 리서치 생성 한도(5건)에 도달했습니다. 내일 다시 시도해주세요.", []
            topic = tool_input.get("topic", "")
            mode = tool_input.get("mode", "standard")
            result = create_fn(user_id, topic, mode)
            if result.get("error"):
                return f"리서치 생성 실패: {result['error']}", []
            rid = result.get("researchId", "")
            return f"리서치가 시작되었습니다!\n\n- **주제**: {topic}\n- **모드**: {mode}\n- **리서치 ID**: {rid}\n- **확인 링크**: /insights/research/{rid}\n\n리서치가 완료되면 Insights 페이지에서 확인하실 수 있습니다.", []
        elif tool_name == "list_accounts":
            user_id = context.get("user_id")
            fn = context.get("list_accounts")
            if not user_id or not fn:
                return "사용자 인증 정보가 없습니다.", []
            return format_accounts(fn(user_id)), []
        elif tool_name == "get_account_insights":
            user_id = context.get("user_id")
            fn = context.get("get_account_insights")
            if not user_id or not fn:
                return "사용자 인증 정보가 없습니다.", []
            res = fn(user_id, tool_input.get("account", ""), tool_input.get("from"), tool_input.get("to"), tool_input.get("types"))
            if res.get("error"):
                return res["error"], []
            return format_account_insights(res), []
        elif tool_name == "get_account_brief":
            user_id = context.get("user_id")
            fn = context.get("get_account_brief")
            if not user_id or not fn:
                return "사용자 인증 정보가 없습니다.", []
            res = fn(user_id, tool_input.get("account", ""))
            if res.get("error"):
                return res["error"], []
            return format_account_brief(res), []
        else:
            return f"Unknown tool: {tool_name}", []
    except Exception as e:
        logger.warning(f"Tool execution failed ({tool_name}): {e}")
        return f"Tool error: {e}", []


def format_kb_results(results):
    """Format KB retrieval results into a readable string."""
    if not results:
        return "Knowledge Base에서 관련 문서를 찾지 못했습니다."
    lines = []
    for r in results:
        uri = r.get("uri", "")
        text = r["text"][:800]
        score = r.get("score", 0)
        lines.append(f"[Score: {score:.2f}] {uri}\n{text}")
    return "\n\n---\n\n".join(lines)


def format_docs_results(results):
    """Format AWS doc search results into a readable string."""
    if not results:
        return "AWS 공식 문서에서 관련 결과를 찾지 못했습니다."
    lines = []
    for d in results:
        lines.append(f"- [{d['title']}]({d['url']}): {d['snippet']}")
    return "AWS 공식 문서 검색 결과:\n" + "\n".join(lines)


def search_in_transcript(keywords, transcript):
    """Search transcript for keyword matches, returning relevant sentences with context."""
    if not transcript:
        return "현재 미팅 트랜스크립트가 없습니다."

    # Split into sentences
    sentences = re.split(r'(?<=[.!?。\n])\s*', transcript)
    sentences = [s.strip() for s in sentences if s.strip()]

    if not sentences:
        return "트랜스크립트가 비어있습니다."

    # Keyword matching (case-insensitive)
    keyword_list = [k.strip().lower() for k in keywords.split() if k.strip()]
    if not keyword_list:
        return "검색 키워드가 비어있습니다."

    matches = []
    for i, sentence in enumerate(sentences):
        lower_sent = sentence.lower()
        if any(kw in lower_sent for kw in keyword_list):
            # Include previous and next sentence for context
            context_parts = []
            if i > 0:
                context_parts.append(sentences[i - 1])
            context_parts.append(f">>> {sentence}")
            if i < len(sentences) - 1:
                context_parts.append(sentences[i + 1])
            matches.append("\n".join(context_parts))

    if not matches:
        return f"트랜스크립트에서 '{keywords}'와 관련된 내용을 찾지 못했습니다."

    # Limit to top 5 matches
    return f"트랜스크립트에서 '{keywords}' 관련 {len(matches)}건 발견:\n\n" + "\n\n---\n\n".join(matches[:5])


def format_meetings_results(meetings):
    """Format meeting list results into a readable string."""
    if not meetings:
        return "조건에 맞는 미팅을 찾지 못했습니다."
    lines = []
    for m in meetings:
        parts = [f"- **{m.get('title', '제목 없음')}** ({m.get('date', '날짜 없음')})"]
        if m.get('isShared'):
            parts.append(f"  [공유받은 미팅, from: {m.get('sharedBy', '알 수 없음')}]")
        if m.get('tags'):
            parts.append(f"  태그: {', '.join(m['tags'])}")
        parts.append(f"  상태: {m.get('status', 'unknown')} | ID: {m.get('meetingId', '')}")
        lines.append('\n'.join(parts))
    return f"미팅 {len(meetings)}건 검색됨:\n\n" + "\n\n".join(lines)


def format_accounts(accounts):
    """Format the user's account list."""
    if not accounts:
        return "속한 고객사(Account)가 없습니다."
    lines = [f"- **{a.get('name', '(이름없음)')}** (역할: {a.get('role', '')}, ID: {a.get('accountId', '')})" for a in accounts]
    return f"내 고객사 {len(accounts)}곳:\n\n" + "\n".join(lines)


def format_account_insights(res):
    """Format account insights grouped chronologically by type."""
    insights = res.get("insights", [])
    name = res.get("account", "")
    if not insights:
        return f"'{name}'에 해당 기간/유형의 인사이트가 없습니다."
    lines = []
    for ins in insights:
        ent = f" [{', '.join(ins['entities'])}]" if ins.get("entities") else ""
        when = (ins.get("occurredAt", "") or "")[:10]
        lines.append(f"- ({ins.get('type', '')}{('/' + when) if when else ''}) {ins.get('text', '')}{ent}")
    return f"'{name}' 인사이트 {len(insights)}건:\n\n" + "\n".join(lines)


def format_account_brief(res):
    """Format the one-shot account brief (meta + insights-by-type + meetings + linked research)."""
    name = res.get("account", "")
    parts = [f"## {name} 브리프"]
    if res.get("industry"):
        parts.append(f"- 산업: {res['industry']}")
    by_type = res.get("insightsByType", {})
    if by_type:
        parts.append("\n### 인사이트 (유형별)")
        for t in sorted(by_type.keys()):
            items = by_type[t]
            parts.append(f"\n**{t}** ({len(items)}건)")
            for ins in items:
                parts.append(f"  - {ins.get('text', '')}")
    else:
        parts.append("\n인사이트 없음.")
    meetings = res.get("meetings", [])
    if meetings:
        parts.append(f"\n### 공유 미팅 {len(meetings)}건")
        for m in meetings:
            when = (m.get("date", "") or "")[:10]
            parts.append(f"  - {m.get('title', '(제목없음)')}{(' (' + when + ')') if when else ''}")
    research = res.get("research", [])
    if research:
        parts.append(f"\n### 연결된 리서치 {len(research)}건")
        for r in research:
            status = r.get("status", "")
            parts.append(f"  - {r.get('topic', '(제목없음)')}{(' [' + status + ']') if status else ''}")
            if r.get("summary"):
                parts.append(f"    {r['summary'][:200]}")
    return "\n".join(parts)

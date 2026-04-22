"""Tool definitions and executor for Bedrock Converse API agentic loop."""

import logging
import re

from aws_docs import search_aws_docs, get_aws_recommendation

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
    }
]


def execute_tool(tool_name, tool_input, context):
    """Execute a tool and return (result_text, source_uris).

    Returns a tuple so the caller can collect sources without re-executing the search.
    """
    try:
        if tool_name == "search_knowledge_base":
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

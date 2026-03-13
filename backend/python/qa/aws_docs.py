import json
import logging
import urllib.request
import urllib.error

logger = logging.getLogger(__name__)

AWS_DOCS_SEARCH_URL = 'https://proxy.search.docs.aws.com/search'


def search_aws_docs(query, limit=3):
    """Search AWS official docs. Returns [{url, title, snippet}]. Never raises — returns [] on failure."""
    try:
        payload = json.dumps({
            'textQuery': {'input': query},
            'contextAttributes': [],
            'locales': ['en_us', 'ko_kr'],
        }).encode('utf-8')

        req = urllib.request.Request(
            AWS_DOCS_SEARCH_URL,
            data=payload,
            headers={'Content-Type': 'application/json'},
            method='POST',
        )

        with urllib.request.urlopen(req, timeout=5) as resp:
            data = json.loads(resp.read().decode('utf-8'))

        results = []
        for suggestion in data.get('suggestions', []):
            excerpt = suggestion.get('textExcerptSuggestion', {})
            url = excerpt.get('link', '')
            title = excerpt.get('title', '')
            snippet = excerpt.get('summary', '')
            if url and title:
                results.append({'url': url, 'title': title, 'snippet': snippet})
                if len(results) >= limit:
                    break
        return results
    except Exception as e:
        logger.warning(f'AWS docs search failed: {e}')
        return []


def get_aws_recommendation(use_case):
    """Get AWS service recommendation — inspired by awsknowledge MCP 'recommend' tool."""
    results = search_aws_docs(f"best practices {use_case}", limit=5)
    if results:
        return "AWS 추천 결과:\n" + "\n".join(
            f"- [{r['title']}]({r['url']}): {r['snippet']}" for r in results
        )
    return "추천 결과를 찾지 못했습니다."

#!/usr/bin/env python3
"""Capture the actual local app UI with synthetic, read-only API fixtures.

Requires Playwright and Pillow in a separate documentation environment.
Start the unchanged frontend with NEXT_PUBLIC_DEV_AUTH=true on loopback first.
Use a system font configuration with Korean coverage (for example, Noto Sans
CJK KR as the system-ui/sans-serif fallback on Linux).
No production authentication, remote API or real microphone is used.
"""

import argparse
import base64
import hashlib
import json
from pathlib import Path
import subprocess
import time
from urllib.parse import urlsplit

STAMP = '2026-11-10T08:00:00Z'
ACCOUNT = {
    'accountId': 'guide-account', 'name': '예시 고객 · 한빛 커머스',
    'description': '가이드용 예시 데이터', 'ownerUserId': 'dev-user',
    'role': 'owner', 'accessPermission': 'edit', 'memberCount': 1,
    'members': [{'userId': 'dev-user', 'name': 'Dev User',
                 'email': 'guide@example.invalid', 'role': 'owner'}],
    'createdAt': STAMP, 'updatedAt': STAMP,
}
PROJECT = {
    'projectId': 'guide-project', 'name': '예시 프로젝트 · 클라우드 전환',
    'ownerUserId': 'dev-user', 'role': 'owner', 'accountIds': ['guide-account'],
    'members': [], 'meetings': [], 'researches': [], 'insights': [],
    'createdAt': STAMP, 'updatedAt': STAMP,
}
MEETING = {
    'meetingId': 'guide-meeting', 'userId': 'dev-user',
    'title': '예시 미팅 · 클라우드 전환 검토', 'status': 'done',
    'date': STAMP, 'createdAt': STAMP, 'updatedAt': STAMP,
    'participants': [{'id': 'guide-person', 'name': '예시 담당자', 'initials': '예'}],
    'duration': 1800, 'tags': ['예시'], 'accountId': 'guide-account',
    'projectIds': [], 'notes': '## 검토 결과\n\n- 단계적으로 서비스를 이전합니다.\n'
    '- 다음 회의에서 비용 기준을 확인합니다.',
    'summary': '## 회의 요약\n\n가이드용 예시 데이터입니다.\n\n'
    '- 서비스 이전 범위와 검토 일정을 논의했습니다.',
    'transcript': '예시 담당자: 단계적으로 전환하는 방안을 검토하겠습니다.',
    'originalTranscript': '예시 담당자: 단계적으로 전환하는 방안을 검토하겠습니다.',
    'segments': [], 'attachments': [], 'actionItems': [],
    'sharedWith': [], 'speakerNames': {}, 'files': [],
}
CONTACT = {
    'contactId': 'guide-contact', 'revision': 'guide-contact-r1',
    'name': '예시 담당자', 'company': '한빛 커머스 (예시)',
    'department': '기술팀', 'title': '기술 담당',
    'email': 'contact@example.invalid', 'notes': '가이드용 예시 연락처',
}
EVENT = {
    'eventId': 'guide-event', 'revision': 'guide-event-r1',
    'title': '예시 일정 · 기술 검토', 'startAt': STAMP,
    'endAt': '2026-11-10T09:00:00Z', 'location': '예시 회의실',
    'attendees': [{'name': '예시 담당자', 'contactId': 'guide-contact'}],
    'createdBy': 'dev-user', 'createdAt': STAMP, 'updatedAt': STAMP,
}
EMPTY_FLOW = {'activity': [], 'response': [], 'history': [],
              'result': {'state': 'UNKNOWN', 'stall': []}}


def fixture(path):
    """Serve only fictional data in the real frontend's normal response shapes."""
    if path == '/api/accounts':
        return {'accounts': [ACCOUNT]}
    if path == '/api/accounts/guide-account':
        return ACCOUNT
    if path == '/api/projects':
        return {'projects': [PROJECT], 'nextCursor': None}
    if path == '/api/projects/guide-project':
        return PROJECT
    if path == '/api/meetings':
        return {'meetings': [MEETING], 'nextCursor': None}
    if path == '/api/meetings/guide-meeting':
        return MEETING
    if path.endswith('/signals'):
        return {'newsState': 'NO_SOURCE', 'news': [], 'opportunities': [],
                'insights': [], 'restrictedCount': 0, 'linksTruncated': False}
    if path.endswith('/flow'):
        return {'rows': [], 'unassigned': EMPTY_FLOW, 'truncated': False}
    if path.endswith('/graph'):
        return {'nodes': [], 'edges': [], 'truncated': False, 'memberCount': 1}
    if path.endswith('/people/mentions'):
        return {'groups': [], 'nextCursor': None, 'truncated': False}
    if path.endswith('/people'):
        return {'people': [{'kind': 'contact', 'id': 'guide-contact',
                           'label': '예시 담당자', 'meetings': 1,
                           'roles': {'participant': 1}}],
                'nextCursor': None, 'truncated': False}
    if path.endswith('/contacts'):
        return {'contacts': [CONTACT], 'nextCursor': None}
    if path.endswith('/speaker-team'):
        return {'members': ACCOUNT['members'], 'nextCursor': None}
    if path.endswith('/events') and '/accounts/' in path:
        return {'events': [EVENT], 'nextCursor': None}
    if path.endswith('/reading'):
        return {'meetingId': 'guide-meeting', 'source': 'notes',
                'content': MEETING['notes'], 'notes': MEETING['notes'],
                'truncated': False, 'nextCursor': None, 'revision': 'guide'}
    if path.endswith('/index-status'):
        return {'state': 'PENDING'}
    if path.endswith('/resummary'):
        return {'status': 'idle'}
    if path.endswith('/action-items'):
        return {'actionItems': [], 'analysis': {'status': 'succeeded'}}
    if path.startswith('/api/calendar/'):
        return {'events': [], 'sources': [], 'nextCursor': None}
    if path.startswith('/api/documents'):
        return {'documents': [], 'nextCursor': None}
    if path.startswith('/api/insights'):
        return {'documents': [], 'insights': [], 'totalCount': 0,
                'services': [], 'sourceIds': [], 'nextCursor': None}
    if path.startswith('/api/research'):
        return {'research': [], 'nextCursor': None}
    if '/dictionary' in path:
        return {'terms': [], 'items': [],
                'vocabularies': {language: {'status': 'NONE', 'activeReady': False,
                                          'termCount': 0}
                                 for language in ('ko', 'en')}}
    if path.endswith('/integrations'):
        return {'notion': {'configured': False}, 'slack': {'configured': False}}
    if '/connectors' in path:
        return {'connectors': [
            {'profile': profile, 'clientId': 'guide-' + profile,
             'enabled': True, 'policyGeneration': 1,
             'maximum': ['read', 'write'] if profile == 'write' else ['read'],
             'approval': None}
            for profile in ('read', 'write')],
                'clients': [], 'grants': [],
                'inventoryVersion': 'guide'}
    if '/meeting-detection' in path:
        return {'enabled': False, 'snoozeMinutes': 30}
    if path.startswith('/api/billing'):
        return []
    return {'items': [], 'files': [], 'sources': [], 'documents': [],
            'insights': [], 'meetings': [], 'projects': [], 'research': [],
            'links': [], 'sessions': [], 'users': [], 'members': [],
            'nextCursor': None}


# slug, route, guide chapter, viewport, action. Every action is UI-only.
SCREENS = [
    ('home', '/', 'home', 'desktop', None),
    ('assistant', '/chat', 'qa', 'desktop', None),
    ('meetings', '/meetings', 'meeting', 'desktop', None),
    ('record', '/record', 'record', 'desktop', None),
    ('upload', '/record?mode=upload', 'upload', 'desktop', None),
    ('meeting', '/meeting/guide-meeting', 'meeting', 'desktop', None),
    ('meeting-followup', '/meeting/guide-meeting', 'followup', 'desktop', '#sa-follow-up'),
    ('accounts', '/accounts', 'accounts', 'desktop', None),
    ('account-overview', '/accounts/guide-account', 'accounts', 'desktop', None),
    ('contacts', '/accounts/guide-account', 'contacts', 'desktop', 'heading:고객 연락처'),
    ('people', '/accounts/guide-account', 'people', 'desktop', '#account-people-heading'),
    ('projects', '/projects', 'projects', 'desktop', None),
    ('project-create', '/projects', 'projects', 'desktop', 'button:새 프로젝트'),
    ('project-detail', '/projects/guide-project', 'projects', 'desktop', None),
    ('documents', '/docs', 'documents', 'desktop', None),
    ('library', '/kb', 'knowledge', 'desktop', None),
    ('library-attachments', '/kb?source=meeting', 'knowledge', 'desktop', None),
    ('research', '/insights?tab=research', 'research', 'desktop', None),
    ('news', '/insights', 'news', 'desktop', None),
    ('tech', '/insights?tab=tech', 'news', 'desktop', None),
    ('dictionary', '/dictionary', 'dictionary', 'desktop', None),
    ('billing', '/billing', 'billing', 'desktop', None),
    ('settings', '/settings', 'settings', 'desktop', None),
    ('settings-import', '/settings#contact-import', 'contacts', 'desktop', '#contact-import'),
    ('admin', '/settings#user-management', 'admin', 'desktop', '#user-management'),
    ('settings-connectors', '/settings', 'settings', 'desktop', 'heading:AI 연결 권한'),
    ('sharing', '/meeting/guide-meeting', 'sharing', 'desktop', 'button:Share'),
    ('simulation', '/meeting/guide-meeting', 'simulation', 'desktop', 'heading:비용·사이징 시뮬레이션 (베타)'),
    ('mobile-home', '/', 'devices', 'mobile', None),
    ('mobile-record', '/record', 'devices', 'mobile', None),
]
SCREENS += [
    ('account-' + tab, '/accounts/guide-account?tab=' + tab, chapter, 'desktop', None)
    for tab, chapter in [('meetings', 'accounts'), ('events', 'calendar'),
                         ('insights', 'accounts'), ('projects', 'accounts'),
                         ('docs', 'accounts'), ('kb', 'knowledge')]
]


def capture(base_url, destination, chromium):
    from PIL import Image
    from playwright.sync_api import sync_playwright

    base = urlsplit(base_url)
    if base.scheme != 'http' or base.hostname not in ('127.0.0.1', 'localhost'):
        raise ValueError('capture requires a local loopback development app')
    if destination.exists():
        raise ValueError('capture output already exists')
    destination.mkdir(parents=True)
    revision = subprocess.check_output(
        ['git', 'rev-parse', 'HEAD'], text=True).strip()
    report = {'sourceRevision': revision, 'mode': 'actual app UI; synthetic API data',
              'appVisualChanges': 'none; screenshot excludes development tooling only',
              'screens': [], 'blockedExternal': [], 'blockedWrites': []}
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True, executable_path=chromium,
                                    args=['--no-sandbox'])
        context = browser.new_context(viewport={'width': 1440, 'height': 1080},
                                      locale='ko-KR', timezone_id='Asia/Seoul',
                                      service_workers='block', reduced_motion='reduce')
        context.add_init_script('localStorage.setItem("theme","light");')
        # This is an invalid, unsigned local fixture, never a server credential.
        # The unchanged API client requires a user/expiry payload before issuing
        # a request. Every request is intercepted below before leaving loopback.
        payload = base64.urlsafe_b64encode(json.dumps({
            'sub': 'dev-user', 'exp': int(time.time()) + 86400,
        }).encode()).decode().rstrip('=')
        context.add_init_script(
            'localStorage.setItem("idToken",' +
            json.dumps('fixture.' + payload + '.invalid') + ');')
        # A blocked external socket must not reach a production QA/provider service.
        def socket_route(socket):
            if urlsplit(socket.url).netloc == base.netloc:
                socket.connect_to_server()
            else:
                socket.close()
        context.route_web_socket('**/*', socket_route)

        def intercept(route):
            request = route.request
            parsed = urlsplit(request.url)
            if (parsed.scheme not in ('http', 'https') or
                    parsed.netloc != base.netloc):
                report['blockedExternal'].append(parsed.hostname)
                route.abort()
            elif parsed.path == '/config.json':
                route.fulfill(json={'cognito': {'region': 'us-east-1',
                                               'userPoolId': 'us-east-1_fixture',
                                               'userPoolClientId': 'fixture'},
                                    'billingInsight': True, 'billingService': True,
                                    'mcp': {'url': '/mcp', 'privateBridgeEnabled': False}})
            elif parsed.path.startswith('/api/'):
                if request.method != 'GET':
                    report['blockedWrites'].append(parsed.path)
                    route.fulfill(status=405, json={'error': {'code': 'READ_ONLY',
                                                             'message': 'Capture is read-only'}})
                else:
                    route.fulfill(json=fixture(parsed.path))
            else:
                route.continue_()

        context.route('**/*', intercept)
        page = context.new_page()
        for slug, route, chapter, viewport, action in SCREENS:
            errors = []
            listener = lambda error: errors.append(str(error))
            page.on('pageerror', listener)
            size = {'width': 390, 'height': 844} if viewport == 'mobile' else {
                'width': 1440, 'height': 1080}
            page.set_viewport_size(size)
            print('Opening', slug, flush=True)
            page.goto(base_url.rstrip('/') + route, wait_until='domcontentloaded')
            page.locator('main').first.wait_for()
            assert page.evaluate(
                'Promise.race([document.fonts.ready.then(()=>true),'
                'new Promise(resolve=>setTimeout(()=>resolve(false),10000))])'
            ), 'app font loading timed out'
            page.wait_for_timeout(400)
            if action:
                if action.startswith('heading:'):
                    page.get_by_role('heading', name=action[8:]).scroll_into_view_if_needed()
                elif action.startswith('button:'):
                    page.get_by_role('button', name=action[7:]).click()
                else:
                    page.locator(action).scroll_into_view_if_needed()
                page.wait_for_timeout(200)
            if errors:
                raise AssertionError(f'{slug}: {errors}')
            # Do not redraw, rewrite DOM, change app styles, or resize the image.
            # Only the Next.js development indicator is outside the product UI.
            raw = page.screenshot(style='nextjs-portal { visibility: hidden !important; }')
            filename = f'screen-{slug}.webp'
            import io
            Image.open(io.BytesIO(raw)).save(destination / filename, 'WEBP', lossless=True)
            body = (destination / filename).read_bytes()
            report['screens'].append({
                'file': filename, 'route': route, 'chapter': chapter,
                'action': action,
                'width': size['width'], 'height': size['height'],
                'bytes': len(body), 'sha256': hashlib.sha256(body).hexdigest(),
                'headings': page.locator('h1,h2,h3').all_text_contents(),
                'errors': errors,
            })
            print(slug, len(body), flush=True)
            (destination / 'capture-report.json').write_text(
                json.dumps(report, ensure_ascii=False, indent=2) + '\n')
            page.remove_listener('pageerror', listener)
        context.close()
        browser.close()
    report['blockedExternal'] = sorted(set(report['blockedExternal']))
    if report['blockedWrites']:
        raise AssertionError('capture unexpectedly attempted an API mutation')
    (destination / 'capture-report.json').write_text(
        json.dumps(report, ensure_ascii=False, indent=2) + '\n')
    return report


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base-url', default='http://127.0.0.1:8782')
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--chromium', required=True)
    options = parser.parse_args()
    capture(options.base_url, options.output, options.chromium)

// In-app release notes: the user-facing subset of CHANGELOG.md for each
// tagged release. CHANGELOG.md (English) stays the complete engineering
// record; these Korean notes ship in the static bundle and render at
// /changelog. Release policy: docs/runbooks/releases.md.
// scripts/release-notes.test.mjs keeps versions, dates, package.json,
// CHANGELOG headings and linked routes aligned.
//
// No imports: the node:test harness evaluates this file in isolation.

export type ReleaseItemKind = 'new' | 'improved' | 'fixed';

export interface ReleaseItem {
  kind: ReleaseItemKind;
  title: string;
  body: string;
  /** How to reach the feature, using the actual UI labels. */
  where: string;
  /** A static app route (served by FrontendStack knownPages), or null. */
  to: string | null;
  /** Only in the TTOBAK Mac app. */
  mac?: boolean;
  /** Pull request numbers in REPO_URL. */
  refs: number[];
}

export interface Release {
  version: string;
  /** Release date (YYYY-MM-DD), equal to the CHANGELOG heading. */
  date: string;
  summary: string;
  items: ReleaseItem[];
}

export const REPO_URL = 'https://github.com/Atom-oh/ttobak';
export const CHANGELOG_PATH = '/changelog';
export const SEEN_KEY = 'ttobak.releaseNotesSeen';

export const RELEASES: Release[] = [
  {
    version: '1.0.0',
    date: '2026-11-11',
    summary: '첫 태그 릴리스입니다. 최근 추가된 주요 기능과 위치를 정리했습니다.',
    items: [
      {
        kind: 'new',
        title: '변경 내역 페이지',
        body: '릴리스마다 바뀐 점과 새 기능의 위치를 이 페이지에서 확인합니다. 새 릴리스가 나오면 사이드바 아래 버전 표시에 점이 붙습니다.',
        where: '사이드바 아래 변경 내역 · 버전, 모바일은 지식 메뉴',
        to: null,
        refs: [903],
      },
      {
        kind: 'new',
        title: 'Home과 미팅 기록 분리',
        body: 'Home은 다음 대화 준비와 고객 인사이트를, Meetings는 전체 미팅 기록과 녹음·업로드를 보여줍니다.',
        where: 'Home / 업무 › Meetings',
        to: '/',
        refs: [640],
      },
      {
        kind: 'improved',
        title: '미팅 목록 먼저, 일정은 옆에',
        body: 'Meetings가 미팅 목록을 먼저 보여주고, 넓은 화면에서는 오른쪽에 일정과 SpecReq 할 일을 둡니다. 좁은 화면에서는 내 일정 · 할 일을 눌러 펼칩니다. 고객별 보기는 고객 이름 가나다순으로 묶습니다.',
        where: '업무 › Meetings → 고객별 / 내 일정 · 할 일',
        to: '/meetings',
        refs: [893],
      },
      {
        kind: 'improved',
        title: '넓은 화면 활용',
        body: '큰 창에서 목록과 대시보드가 전체 폭을 쓰고, 모바일 하단 메뉴가 Assistant 입력창과 목록 끝 버튼을 가리지 않습니다.',
        where: '모든 작업 화면',
        to: null,
        refs: [889],
      },
      {
        kind: 'new',
        title: '영문 회의록',
        body: '편집 권한이 있으면 완료된 미팅의 AI 요약을 영문으로 만들 수 있고, 만들어진 뒤에는 누구나 한국어와 English를 바꿔 봅니다.',
        where: 'Meetings → 미팅 선택 → AI Summary → 영문 회의록 생성, 한국어 / English',
        to: '/meetings',
        refs: [802, 807, 808, 809],
      },
      {
        kind: 'new',
        title: 'SA 활동 대시보드와 리포트',
        body: '나만 보는 SA 활동 페이지에서 스코어카드, 놓친 SFDC 기록, 계정 매핑 대기열을 확인합니다. 리포트 탭에서 저장된 리포트를 보고, Mac 앱에서는 기간을 정해 새 리포트를 만듭니다.',
        where: '업무 › SA Activity → 대시보드 / 리포트',
        to: '/performance',
        refs: [817, 882, 883],
      },
      {
        kind: 'new',
        title: 'Mac 로컬 수집',
        body: 'Mac 앱이 이 컴퓨터에 연결된 일정, SFDC 같은 업무 데이터를 수집해 SA 활동에 반영합니다. 자동 수집 주기를 정하거나 지금 바로 수집합니다.',
        where: 'Settings → Mac 로컬 수집, SA Activity → 데이터 수집 상태 → 지금 수집',
        to: '/settings',
        mac: true,
        refs: [860, 862],
      },
      {
        kind: 'new',
        title: '고객 개요: AWS 시그널, 기회별 흐름, 관계 그래프',
        body: '고객의 최근 뉴스·기회 시그널, 기회별 활동→결과 흐름과 단계 변경, 한 사람이나 프로젝트를 중심으로 연결된 미팅과 기술을 한 화면에서 봅니다. 고객의 직접 멤버에게 보입니다.',
        where: 'Accounts → 고객 선택 → 개요',
        to: '/accounts',
        refs: [718, 719, 877],
      },
      {
        kind: 'new',
        title: '관련 인물과 화자 연결',
        body: '미팅에 나온 이름을 고객 인물에 연결하거나 사람이 아니라고 표시합니다. 고객에 공유된 미팅에서는 화자를 Account 팀이나 고객 연락처에 연결할 수 있습니다.',
        where: 'Accounts → 고객 선택 → 개요 → 관련 인물, 미팅의 화자 이름 설정 → 추천 선택',
        to: '/accounts',
        refs: [744],
      },
      {
        kind: 'improved',
        title: '고객 계층 트리',
        body: 'Accounts 왼쪽에서 상위·하위 고객을 트리로 펼쳐 고릅니다. 상위에서 공유된 고객은 읽기 전용으로 표시됩니다.',
        where: 'Accounts → 고객 계층',
        to: '/accounts',
        refs: [852],
      },
      {
        kind: 'new',
        title: '어카운트 병합과 소유권 이전',
        body: '소유자는 미리보기로 옮겨지는 항목을 확인한 뒤 다른 어카운트로 병합하고, 멤버에게 소유권을 넘길 수 있습니다. 이전 소유자는 SA가 됩니다.',
        where: 'Accounts → 고객 선택 → 개요 → 다른 어카운트로 병합, Members → 소유권 이전',
        to: '/accounts',
        refs: [819, 835, 837],
      },
      {
        kind: 'new',
        title: '연락처 가져오기',
        body: 'Remember·CSV·Excel 연락처 파일을 올리면 회사별로 고객을 맞춰 가져오고, 바뀐 연락처는 확인 후 적용합니다.',
        where: 'Settings → 연락처 가져오기, 고객 개요 → 고객 연락처',
        to: '/settings',
        refs: [677, 858],
      },
      {
        kind: 'new',
        title: '녹음 중 고객·프로젝트 연결',
        body: '녹음하는 동안 고객과 프로젝트를 고르면 미팅이 생성될 때 연결됩니다.',
        where: 'New Meeting → 실시간 녹음 → 녹음 중 고객 연결 / 프로젝트 추가',
        to: '/record',
        refs: [648],
      },
      {
        kind: 'new',
        title: 'Internal 미팅 분류',
        body: '팀, 1:1, 교육·세미나, 기타 내부 미팅을 고객 미팅과 구분하고 미팅 목록에서 분류별로 걸러 봅니다.',
        where: '미팅 → Account → 미팅 분류, Meetings → 미팅 분류 필터',
        to: '/meetings',
        refs: [724],
      },
      {
        kind: 'new',
        title: 'SpecReq 할 일',
        body: '미팅 후 처리할 SpecReq 작업을 나에게만 보여주고, Slack 요청 문구 복사와 어카운트·담당자 지정을 바로 합니다.',
        where: 'Home 또는 Meetings → SpecReq 할 일',
        to: '/',
        refs: [775],
      },
      {
        kind: 'new',
        title: '회의 자막 창',
        body: '미팅 중 언어별 자막 창을 화면 위에 띄웁니다. 글꼴, 크기, 색과 배경 불투명도를 바꿀 수 있습니다. Zoom에서는 전체 화면을 공유해야 자막이 함께 보입니다.',
        where: 'New Meeting → 실시간 녹음 → 자막 창',
        to: '/record',
        mac: true,
        refs: [788, 803],
      },
      {
        kind: 'improved',
        title: '녹음 화면 구성',
        body: '녹음 중 통역, 실시간 요약, 메모, 실시간 자막, Q&A·자료 카드 중 보일 카드를 고릅니다.',
        where: '실시간 녹음 중 화면 구성 (데스크톱)',
        to: '/record',
        refs: [804],
      },
      {
        kind: 'new',
        title: '회의 감지 후 녹음 제안',
        body: '회의 앱이나 브라우저의 회의 탭이 마이크를 쓰기 시작하면 떠 있는 창에서 바로 녹음을 시작합니다. 감지만으로 녹음하지는 않습니다.',
        where: 'Settings → 회의 감지 (Mac 앱)',
        to: '/settings',
        mac: true,
        refs: [679, 722],
      },
      {
        kind: 'improved',
        title: '음소거 따라가기와 녹음 중 마이크 변경',
        body: 'Zoom·Meet에서 음소거하면 내 목소리를 녹음하지 않고, 화면에 음소거와 사용 중인 마이크를 표시합니다. 녹음 중에도 마이크를 바꿀 수 있습니다.',
        where: '실시간 녹음 → System → 녹음할 마이크',
        to: '/record',
        mac: true,
        refs: [621, 667, 725],
      },
      {
        kind: 'new',
        title: 'Mac 앱 업데이트',
        body: 'Mac 앱이 새 버전을 확인하고 업데이트를 제안합니다. 녹음 중에는 업데이트하지 않습니다.',
        where: 'Mac 앱 메뉴 → 업데이트 확인…',
        to: null,
        mac: true,
        refs: [764],
      },
      {
        kind: 'new',
        title: '사용 설명서',
        body: '화면별 사용법, AI 동작 원리, 문제 해결을 앱 안에서 봅니다.',
        where: '사이드바 Settings 아래 사용 설명서, 상단 도움말 아이콘',
        to: null,
        refs: [811],
      },
      {
        kind: 'improved',
        title: '뉴스 출처 표시와 묶어 보기',
        body: '뉴스마다 공통 또는 고객, 소스, 검색어와 관련도를 표시하고, 공통과 고객별로 묶어 봅니다. 고객 소스 뉴스는 그 고객의 직접 멤버에게만 보입니다.',
        where: '지식 › News & Updates → 뉴스 보기 방식',
        to: '/insights',
        refs: [886, 891],
      },
      {
        kind: 'new',
        title: '프로젝트 단계',
        body: 'Prospect부터 Launched, Closed Lost까지 SFDC와 같은 단계로 프로젝트를 관리합니다. 단계 변경 이력은 고객 개요의 기회별 흐름에 나옵니다.',
        where: 'Projects → 프로젝트 선택 → 단계',
        to: '/projects',
        refs: [708],
      },
      {
        kind: 'new',
        title: '이름 수정',
        body: '내 표시 이름을 직접 바꾸고, 관리자는 사용자 관리에서 다른 사용자의 이름을 바꿉니다.',
        where: 'Profile → 이름, 관리자는 Settings → 사용자 관리',
        to: '/profile',
        refs: [880],
      },
      {
        kind: 'fixed',
        title: '멈춘 전사 줄이기',
        body: '서버 회수로 전사가 멈춘 채 남던 문제를 줄였습니다. 첫 전사는 중단되지 않는 서버에서 실행하고, 멈춘 단일 파일 녹음은 자동으로 다시 전사합니다.',
        where: '녹음·업로드 후 자동 처리',
        to: '/meetings',
        refs: [888],
      },
      {
        kind: 'fixed',
        title: '요약 시간 링크로 오디오 이동',
        body: 'AI 요약의 시간 링크를 누르면 해당 발화로 이동하고 그 위치부터 오디오를 재생합니다.',
        where: '미팅 → AI Summary의 시간 링크',
        to: '/meetings',
        refs: [598, 610],
      },
      {
        kind: 'fixed',
        title: '저장한 마이크가 없을 때 기본 마이크 사용',
        body: '이전에 고른 마이크가 연결되어 있지 않으면 녹음이 실패하지 않고 기본 마이크로 녹음합니다.',
        where: '실시간 녹음 → 마이크',
        to: '/record',
        refs: [885],
      },
      {
        kind: 'fixed',
        title: 'Mac 앱의 확인 창',
        body: 'Mac 앱에서 삭제 확인 같은 확인·알림 창이 앱 안 대화상자로 정상 표시됩니다.',
        where: '확인이 필요한 모든 작업',
        to: null,
        mac: true,
        refs: [619],
      },
    ],
  },
];

export const LATEST_RELEASE = RELEASES[0];

interface SeenStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

// Storage can be absent (static prerender) or throw from the getter itself
// (private mode, blocked site data).
function defaultStorage(): SeenStorage | undefined {
  try {
    return typeof window === 'undefined' ? undefined : window.localStorage;
  } catch {
    return undefined;
  }
}

export function seenVersion(storage: SeenStorage | undefined = defaultStorage()): string | null {
  try {
    return storage?.getItem(SEEN_KEY) ?? null;
  } catch {
    return null;
  }
}

const listeners = new Set<() => void>();

export function markSeen(version: string = LATEST_RELEASE.version, storage: SeenStorage | undefined = defaultStorage()): void {
  try {
    storage?.setItem(SEEN_KEY, version);
  } catch {
    // Without storage the unseen dot simply stays visible.
  }
  listeners.forEach(listener => listener());
}

export function hasUnseenRelease(storage?: SeenStorage): boolean {
  return seenVersion(storage ?? defaultStorage()) !== LATEST_RELEASE.version;
}

/** useSyncExternalStore subscription: this tab's markSeen and other tabs' storage events. */
export function subscribeReleaseSeen(listener: () => void): () => void {
  listeners.add(listener);
  const onStorage = (event: { key: string | null }) => {
    if (event.key === SEEN_KEY || event.key === null) listener();
  };
  if (typeof window !== 'undefined') window.addEventListener('storage', onStorage);
  return () => {
    listeners.delete(listener);
    if (typeof window !== 'undefined') window.removeEventListener('storage', onStorage);
  };
}

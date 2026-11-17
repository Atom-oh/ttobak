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
    version: '1.1.0',
    date: '2026-11-17',
    summary: '미팅 전 고객 요약, 고객 뉴스 지금 수집, 고객 초안, 일정의 고객 공유, Mac 한 번에 설정과 통역 화면 개선이 추가됐습니다.',
    items: [
      {
        kind: 'new',
        title: '미팅 전 한 장 요약',
        body: '고객 개요에서 다음 미팅, 지난 미팅 이후 변화, 최근 뉴스, 물어볼 질문, 낯선 용어를 출처와 함께 한 장으로 봅니다. 낯선 용어는 그 고객의 사전에 바로 추가할 수 있습니다. 고객의 직접 멤버에게 보입니다.',
        where: 'Accounts → 고객 선택 → 개요 → 요약 만들기',
        to: '/accounts',
        refs: [915, 917, 924],
      },
      {
        kind: 'new',
        title: '고객 뉴스 지금 수집',
        body: '고객 전용 뉴스 소스를 한 시간에 한 번 바로 수집합니다. 뉴스 소스가 없는 고객은 고객 이름으로 소스를 한 번에 만들어 수집을 시작합니다. 매일 정기 수집 시간과 다른 수집이 진행 중일 때는 요청할 수 없습니다.',
        where: 'Insights → 뉴스 → 소스 → 지금 수집',
        to: '/insights',
        refs: [919, 921, 922, 926, 930, 942],
      },
      {
        kind: 'new',
        title: '고객 초안',
        body: '따라가는 소스의 새 링크를 읽고 고객별로 전할 만한 업데이트가 있으면 나만 보는 초안을 만듭니다. 복사, 완료, 숨기기로 정리하며 자동으로 보내지는 않습니다.',
        where: '사이드바 › 고객 초안, Accounts → 고객 선택 → 초안 탭',
        to: '/drafts',
        refs: [957, 961, 970, 988],
      },
      {
        kind: 'new',
        title: '일정의 고객 공유',
        body: 'Mac 수집이 Outlook 일정을 고객과 맞춰, 한 고객에 맞고 비공개가 아닌 일정만 고객 팀에 공유합니다. 다가오는 일정에 공유됨·후보·비공개 표시와 고객사 지정·공유 해제가 생기고, 고객의 직접 멤버는 고객 일정을 봅니다. Private 분류 일정은 공유하지 않습니다.',
        where: 'Home → 다가오는 일정, Accounts → 고객 선택 → 일정',
        to: '/',
        mac: true,
        refs: [842, 934, 843],
      },
      {
        kind: 'improved',
        title: '고객 미팅 먼저, 녹음 자동 연결',
        body: '다가오는 일정에서 고객 미팅을 먼저 보여주고 사내·종일 일정은 사내 일정 N건으로 접습니다. 고객 일정 시작 10분 전부터 끝날 때까지 녹음을 시작하면 그 고객에 자동으로 연결되며, 표시된 칩에서 바꿀 수 있습니다.',
        where: 'Home → 다가오는 일정, 녹음 화면의 고객 칩 → 변경',
        to: '/record',
        refs: [927],
      },
      {
        kind: 'improved',
        title: '미팅 목록에서 바로 고객 지정',
        body: '고객이 지정되지 않은 내 미팅의 오른쪽에서 고객을 고르면 바로 그 고객으로 분류됩니다. 팀 공유는 하지 않으며, 공유는 미팅 상세에서 따로 합니다.',
        where: '업무 › Meetings → 고객 미지정 → 고객 선택…',
        to: '/meetings',
        refs: [986, 987, 996],
      },
      {
        kind: 'new',
        title: 'SpecReq 작업: 고객 미팅 아님',
        body: '고객이나 담당자가 필요한 SpecReq 작업이 고객 미팅이 아니면 고객 미팅 아님으로 닫습니다.',
        where: 'Meetings → 내 일정 · 할 일 → 고객 미팅 아님',
        to: '/meetings',
        refs: [951],
      },
      {
        kind: 'new',
        title: 'SFDC 연락처와 동기화 옵션',
        body: 'Mac 수집이 연결된 고객의 SFDC 연락처를 고객 연락처에 연결하거나 추가하고, 연락처에 SFDC 표시를 붙입니다. 이메일과 전화번호는 Mac 밖으로 나가지 않습니다. 동기화 옵션에서 어떤 응답의 일정을 가져올지와 SFDC 연락처 가져오기를 정합니다.',
        where: 'Settings → Mac 로컬 수집 → 동기화 옵션',
        to: '/settings',
        mac: true,
        refs: [910, 918, 920, 923],
      },
      {
        kind: 'new',
        title: 'Mac 한 번에 설정과 스킬 패키지',
        body: 'Mac 앱에서 버튼 하나로 PATH 정리, 어댑터 업데이트, 스킬과 MCP 서버 설치, 수집 설정을 차례로 진행합니다. 새 MCP 서버는 먼저 승인을 받습니다. Settings에서 개인 스킬 패키지를 올리고 지우며, 관리자는 조직 패키지 버전을 관리합니다.',
        where: 'Settings → Mac 로컬 수집 → 한 번에 설정, Settings → 스킬 패키지',
        to: '/settings',
        mac: true,
        refs: [931, 933, 935, 936, 937, 939],
      },
      {
        kind: 'improved',
        title: 'SA 리포트에서 뺄 Slack 채널',
        body: '최근 90일 동안 글을 쓴 Slack 채널 중 리포트에 넣지 않을 채널을 고릅니다.',
        where: 'SA Activity → 리포트 만들기 → 고급 설정',
        to: '/performance',
        mac: true,
        refs: [932, 938],
      },
      {
        kind: 'improved',
        title: '통역 화면: 자막 창, 말하는 언어, 화자 색',
        body: '통역 화면에서 자막 창을 열고 자막 창 언어별로 큰 글씨 자막을 봅니다. 말하는 언어를 한두 개 고르고, 자막은 화자별 색으로 구분하며 가장 많이 말한 사람은 기본 색으로 보입니다. 자막 창에는 닫기 버튼과 둥근 모서리가 생겼습니다.',
        where: '통역 화면, 떠 있는 자막 창 (Mac 앱 0.2.2 이상)',
        to: '/interpret',
        mac: true,
        refs: [949, 953, 954, 955, 958, 966, 968, 993],
      },
      {
        kind: 'improved',
        title: '한글 발음으로 용어 인식',
        body: '사용자 사전의 인식된 표기에 한글로 적은 발음이 배치 전사와 실시간 자막 인식에도 쓰이고, 영문 이름 용어의 한글 발음은 자동으로 만들어집니다. 어휘가 다시 만들어진 뒤 적용됩니다.',
        where: 'Dictionary → 용어 → 인식된 표기',
        to: '/dictionary',
        refs: [990, 991, 992],
      },
      {
        kind: 'improved',
        title: 'Remember 연락처 끌어다 놓기',
        body: 'Remember에서 내보낸 CSV나 xlsx 파일을 연락처 가져오기 영역에 끌어다 놓습니다. Mac 앱의 다른 업로드 영역도 끌어다 놓기를 받습니다.',
        where: 'Accounts → 고객 선택 → Remember / CSV / Excel 가져오기, Settings',
        to: '/accounts',
        refs: [959],
      },
      {
        kind: 'fixed',
        title: 'Mac 앱 안정화',
        body: '알림 소리에 미팅 감지가 잘못 뜨지 않고, 조용한 Mac에서도 통역이 시작되며, 잠자기에 들어가면 통역이 확실히 멈춥니다. 페이지를 떠나면 캡처가 멈추고 사이드바 끝에서 화면이 뒤로 스크롤되지 않습니다.',
        where: 'Mac 앱',
        to: null,
        mac: true,
        refs: [945, 952, 956, 965, 969, 981],
      },
    ],
  },
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
        title: '뉴스 소스 관리',
        body: '공통 뉴스와 고객사 전용 뉴스를 한곳에서 등록하고, 고객사 전용 소스는 이름과 검색어를 수정합니다. 뉴스 목록은 전체, 공통, 고객사별 범위로 걸러 봅니다.',
        where: '지식 › News & Updates → 뉴스 소스 관리, 고객 상세 → 뉴스 소스 등록',
        to: '/insights',
        refs: [895, 899],
      },
      {
        kind: 'improved',
        title: '사전 용어를 자막·번역·전사에 반영',
        body: '내 사전의 용어를 실시간 자막 인식과 고품질 실시간 번역에 사용하고, 회의록을 만들 때 잘못 들린 용어를 사전 기준으로 바로잡습니다. 회의마다 IT·업계·규제 용어를 더 많이 모으고, 고객사 사전과 AWS 기술 문서 용어도 쌓습니다.',
        where: '지식 › Custom Dictionary, 녹음 중 자막·번역, 회의록 생성',
        to: '/dictionary',
        refs: [896, 897, 898, 900, 902, 911, 912, 914],
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
      {
        kind: 'fixed',
        title: '리포트 수집 도구 업데이트 안내',
        body: '수집 도구가 예전 버전이라 리포트를 만들 수 없을 때, 일반 오류 대신 설정의 수집 섹션에서 업데이트하라고 알려 줍니다.',
        where: 'Mac 앱 › 리포트 만들기',
        to: null,
        mac: true,
        refs: [904],
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

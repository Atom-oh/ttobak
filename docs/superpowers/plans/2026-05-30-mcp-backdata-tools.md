# MCP Back-Data Tools — Implementation Plan (Plan 4 of 6)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development or superpowers:executing-plans. Steps use `- [ ]` checkboxes.

**Goal:** 개인 맥북 에이전트가 Account 단위 원재료를 MCP로 빨아갈 수 있게, TTOBAK MCP 서버(TypeScript)에 Account 스코프 도구를 추가한다. 대부분 기존 REST 엔드포인트(Plan 1-3)를 재사용하고, "일괄 소비"용 `account_brief` 하나만 Go 신규 엔드포인트로 합성한다.

**Architecture:** Go쪽은 `GetAccountBrief`(기존 `GetAccount`+`ListAccountMeetings`+`ListAccountInsights` 합성, 멤버 게이트 상속) + 핸들러/라우트 `GET /api/accounts/{id}/brief`. TS쪽은 `mcp-server/src/api.ts`에 5개 메서드(`listAccounts`/`getAccount`/`getAccountMeetings`/`getAccountInsights`/`getAccountBrief`), `index.ts`에 5개 도구 정의+switch case, README 갱신.

**Tech Stack:** Go 1.25(stdlib testing) + TypeScript(`tsc`, 테스트 프레임워크 없음 → `npm run build` 타입체크로 검증).

**선행:** Plan 1-3 완료, branch `feat/account-foundation`.

## File Structure (Plan 4)
| 파일 | 변경 |
|---|---|
| `backend/internal/model/account.go` | `AccountBrief` 구조체 |
| `backend/internal/service/account.go` | `GetAccountBrief` |
| `backend/internal/service/account_test.go` | `GetAccountBrief` 테스트 |
| `backend/internal/handler/account.go` | `GetAccountBrief` 핸들러 |
| `backend/internal/handler/account_test.go` | 핸들러 테스트 |
| `backend/cmd/api/main.go` | brief 라우트 |
| `docs/API-SPEC.md` | brief 엔드포인트 |
| `mcp-server/src/api.ts` | 5개 API 메서드 |
| `mcp-server/src/index.ts` | 5개 도구 정의 + switch case |
| `mcp-server/README.md` | 도구 표 2곳 + `/mcp` 목록 2곳 |

---

## Task 1: AccountBrief 모델 + GetAccountBrief 서비스

**Files:**
- Modify: `backend/internal/model/account.go`, `backend/internal/service/account.go`, `account_test.go`

- [ ] **Step 1: 모델 추가** (`account.go` 끝)

```go
// AccountBrief bundles an account's raw material for one-shot consumption by
// the personal-side agent (SFDC/SIFT/2by2/Player Card prep). Insights are
// grouped by type for convenience.
type AccountBrief struct {
	Account        *AccountResponse                `json:"account"`
	InsightsByType map[string][]AccountInsightDTO  `json:"insightsByType"`
	Meetings       []MeetingRefDTO                 `json:"meetings"`
}
```

- [ ] **Step 2: 실패 테스트 작성** (`account_test.go`)

```go
func TestGetAccountBrief_Bundles(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	d := time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC)
	repo.insightsByAccount[acc.AccountID] = []model.AccountInsight{
		{AccountID: acc.AccountID, Type: model.InsightRisk, Text: "지연", OccurredAt: d},
		{AccountID: acc.AccountID, Type: model.InsightRisk, Text: "지연2", OccurredAt: d},
		{AccountID: acc.AccountID, Type: model.InsightOpportunity, Text: "확대", OccurredAt: d},
	}
	repo.meetingRefs[acc.AccountID] = []model.MeetingRef{{AccountID: acc.AccountID, MeetingID: "m-1", Title: "ROSA"}}

	brief, err := svc.GetAccountBrief(context.Background(), "owner-1", acc.AccountID, time.Time{}, time.Time{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if brief.Account == nil || brief.Account.Name != "하나은행" {
		t.Errorf("missing account meta: %+v", brief.Account)
	}
	if len(brief.InsightsByType[model.InsightRisk]) != 2 || len(brief.InsightsByType[model.InsightOpportunity]) != 1 {
		t.Errorf("insights not grouped: %+v", brief.InsightsByType)
	}
	if len(brief.Meetings) != 1 || brief.Meetings[0].MeetingID != "m-1" {
		t.Errorf("missing meetings: %+v", brief.Meetings)
	}
}

func TestGetAccountBrief_NonMemberForbidden(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	_, err := svc.GetAccountBrief(context.Background(), "stranger-9", acc.AccountID, time.Time{}, time.Time{}, nil)
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}
```

- [ ] **Step 3: 실패 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestGetAccountBrief ./internal/service/`
Expected: FAIL — `svc.GetAccountBrief undefined`.

- [ ] **Step 4: 구현** (`account.go`)

```go
// GetAccountBrief composes account meta + insights (grouped by type) + shared
// meetings into one payload. Access is enforced by the composed methods
// (GetAccount gates membership first).
func (s *AccountService) GetAccountBrief(ctx context.Context, userID, accountID string, from, to time.Time, types []string) (*model.AccountBrief, error) {
	account, err := s.GetAccount(ctx, userID, accountID)
	if err != nil {
		return nil, err
	}
	meetings, err := s.ListAccountMeetings(ctx, userID, accountID)
	if err != nil {
		return nil, err
	}
	insights, err := s.ListAccountInsights(ctx, userID, accountID, from, to, types)
	if err != nil {
		return nil, err
	}
	byType := make(map[string][]model.AccountInsightDTO)
	for _, ins := range insights {
		byType[ins.Type] = append(byType[ins.Type], ins)
	}
	return &model.AccountBrief{Account: account, InsightsByType: byType, Meetings: meetings}, nil
}
```

- [ ] **Step 5: 통과 확인**

Run: `cd backend && /usr/local/go/bin/go test -run TestGetAccountBrief ./internal/service/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd backend && git add internal/model/account.go internal/service/account.go internal/service/account_test.go
git commit -m "feat(mcp): GetAccountBrief composes meta+insights+meetings"
```

---

## Task 2: 핸들러 + 라우트 + API-SPEC

**Files:**
- Modify: `backend/internal/handler/account.go`, `account_test.go`, `backend/cmd/api/main.go`, `docs/API-SPEC.md`

- [ ] **Step 1: 핸들러 작성** (`handler/account.go` 끝) — insights 핸들러의 쿼리 파싱을 그대로 재사용

```go
func (h *AccountHandler) GetAccountBrief(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := middleware.GetUserID(ctx)
	accountID := chi.URLParam(r, "accountId")
	if accountID == "" {
		writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "Account ID is required")
		return
	}
	var from, to time.Time
	if v := r.URL.Query().Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid 'from' (RFC3339)")
			return
		}
		from = t
	}
	if v := r.URL.Query().Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, model.ErrCodeBadRequest, "invalid 'to' (RFC3339)")
			return
		}
		to = t
	}
	var types []string
	if v := r.URL.Query().Get("types"); v != "" {
		types = strings.Split(v, ",")
	}
	brief, err := h.accountService.GetAccountBrief(ctx, userID, accountID, from, to, types)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrForbidden):
			writeError(w, http.StatusForbidden, model.ErrCodeForbidden, "Access denied")
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusNotFound, model.ErrCodeNotFound, "Account not found")
		default:
			writeError(w, http.StatusInternalServerError, model.ErrCodeInternalError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, brief)
}
```

- [ ] **Step 2: 핸들러 테스트** (`handler/account_test.go`)

```go
func TestHandlerGetAccountBrief_Forbidden(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}

	r := httptest.NewRequest(http.MethodGet, "/api/accounts/acc-1/brief", nil)
	r = withUserEmailCtx(r, "stranger-9", "s@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	w := httptest.NewRecorder()

	h.GetAccountBrief(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", w.Code, w.Body.String())
	}
}
```

- [ ] **Step 3: 라우트 등록** (`cmd/api/main.go`, account 라우트 옆)

```go
		r.Get("/api/accounts/{accountId}/brief", accountHandler.GetAccountBrief)
```

- [ ] **Step 4: 빌드 + 테스트 + ARM64**

```bash
cd backend && /usr/local/go/bin/go build ./... \
  && /usr/local/go/bin/go test ./internal/... \
  && GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o cmd/api/bootstrap ./cmd/api
```
Expected: 모두 성공. (bootstrap 커밋 금지 — gitignore.)

- [ ] **Step 5: API-SPEC**

`GET /api/accounts/{accountId}/brief?from&to&types` → `{account:{...}, insightsByType:{risk:[...],...}, meetings:[...]}` (403/404/400).

- [ ] **Step 6: Commit**

```bash
cd backend && git add internal/handler/account.go internal/handler/account_test.go cmd/api/main.go && git -C .. add docs/API-SPEC.md
git commit -m "feat(mcp): GET /api/accounts/{id}/brief endpoint + API-SPEC"
```

---

## Task 3: TtobakApi 메서드 5개 (api.ts)

mcp-server는 테스트 프레임워크가 없으므로 검증은 `npm run build`(tsc 타입체크). 기존 `this.get(...)`/`URLSearchParams` 패턴을 따른다.

**Files:**
- Modify: `mcp-server/src/api.ts`

- [ ] **Step 1: `TtobakApi` 클래스에 메서드 추가** (`getMeeting` 다음, `private get` 앞)

```ts
  async listAccounts() {
    return this.get('/api/accounts');
  }

  async getAccount(accountId: string) {
    return this.get(`/api/accounts/${accountId}`);
  }

  async getAccountMeetings(accountId: string) {
    return this.get(`/api/accounts/${accountId}/meetings`);
  }

  async getAccountInsights(
    accountId: string,
    opts?: { from?: string; to?: string; types?: string[] },
  ) {
    const q = new URLSearchParams();
    if (opts?.from) q.set('from', opts.from);
    if (opts?.to) q.set('to', opts.to);
    if (opts?.types && opts.types.length) q.set('types', opts.types.join(','));
    const qs = q.toString();
    return this.get(`/api/accounts/${accountId}/insights${qs ? '?' + qs : ''}`);
  }

  async getAccountBrief(
    accountId: string,
    opts?: { from?: string; to?: string; types?: string[] },
  ) {
    const q = new URLSearchParams();
    if (opts?.from) q.set('from', opts.from);
    if (opts?.to) q.set('to', opts.to);
    if (opts?.types && opts.types.length) q.set('types', opts.types.join(','));
    const qs = q.toString();
    return this.get(`/api/accounts/${accountId}/brief${qs ? '?' + qs : ''}`);
  }
```

- [ ] **Step 2: 타입체크**

Run: `cd mcp-server && npm install >/dev/null 2>&1; npm run build`
Expected: 성공(에러 없음). (node_modules 없으면 install이 채움.)

- [ ] **Step 3: Commit**

```bash
cd mcp-server && git add src/api.ts
git commit -m "feat(mcp): TtobakApi account back-data methods"
```

---

## Task 4: 도구 정의 + switch case (index.ts)

**Files:**
- Modify: `mcp-server/src/index.ts`

- [ ] **Step 1: `tools` 배열에 5개 추가** (`ttobak_get_meeting` 다음에 삽입)

```ts
    {
      name: 'ttobak_list_accounts',
      description: 'List customer accounts you belong to (id, name, your role). Entry point for account-scoped queries.',
      inputSchema: { type: 'object' as const, properties: {} },
    },
    {
      name: 'ttobak_get_account',
      description: 'Get account detail: name, aliases, domains, industry, members and roles.',
      inputSchema: {
        type: 'object' as const,
        properties: { accountId: { type: 'string', description: 'Account ID' } },
        required: ['accountId'],
      },
    },
    {
      name: 'ttobak_get_account_meetings',
      description: 'List meetings shared into an account (meetingId, title, owner, date).',
      inputSchema: {
        type: 'object' as const,
        properties: { accountId: { type: 'string', description: 'Account ID' } },
        required: ['accountId'],
      },
    },
    {
      name: 'ttobak_get_account_insights',
      description:
        'Get typed field insights for an account (raw material for SIFT / 2by2). Filter by period and insight types (trend, need, competitive, risk, opportunity, tech, stakeholder, action).',
      inputSchema: {
        type: 'object' as const,
        properties: {
          accountId: { type: 'string', description: 'Account ID' },
          from: { type: 'string', description: 'Optional start (RFC3339, e.g. 2026-05-01T00:00:00Z)' },
          to: { type: 'string', description: 'Optional end (RFC3339)' },
          types: { type: 'array', items: { type: 'string' }, description: 'Optional insight types to include' },
        },
        required: ['accountId'],
      },
    },
    {
      name: 'ttobak_get_account_brief',
      description:
        'Get bundled raw material for an account in one call: meta + insights grouped by type + shared meetings. Best for preparing SFDC/SIFT/2by2/Player Card on the personal side.',
      inputSchema: {
        type: 'object' as const,
        properties: {
          accountId: { type: 'string', description: 'Account ID' },
          from: { type: 'string', description: 'Optional start (RFC3339)' },
          to: { type: 'string', description: 'Optional end (RFC3339)' },
          types: { type: 'array', items: { type: 'string' }, description: 'Optional insight types to include' },
        },
        required: ['accountId'],
      },
    },
```

- [ ] **Step 2: switch에 5개 case 추가** (`ttobak_get_meeting` case 다음)

```ts
      case 'ttobak_list_accounts': {
        const result = await api.listAccounts();
        return text(JSON.stringify(result, null, 2));
      }

      case 'ttobak_get_account': {
        const { accountId } = args as { accountId: string };
        if (!accountId) return error('accountId is required');
        const result = await api.getAccount(accountId);
        return text(JSON.stringify(result, null, 2));
      }

      case 'ttobak_get_account_meetings': {
        const { accountId } = args as { accountId: string };
        if (!accountId) return error('accountId is required');
        const result = await api.getAccountMeetings(accountId);
        return text(JSON.stringify(result, null, 2));
      }

      case 'ttobak_get_account_insights': {
        const { accountId, from, to, types } = args as {
          accountId: string;
          from?: string;
          to?: string;
          types?: string[];
        };
        if (!accountId) return error('accountId is required');
        const result = await api.getAccountInsights(accountId, { from, to, types });
        return text(JSON.stringify(result, null, 2));
      }

      case 'ttobak_get_account_brief': {
        const { accountId, from, to, types } = args as {
          accountId: string;
          from?: string;
          to?: string;
          types?: string[];
        };
        if (!accountId) return error('accountId is required');
        const result = await api.getAccountBrief(accountId, { from, to, types });
        return text(JSON.stringify(result, null, 2));
      }
```

- [ ] **Step 3: 타입체크**

Run: `cd mcp-server && npm run build`
Expected: 성공.

- [ ] **Step 4: Commit**

```bash
cd mcp-server && git add src/index.ts
git commit -m "feat(mcp): account back-data tool definitions + handlers"
```

---

## Task 5: README 갱신 + 최종 검증

**Files:**
- Modify: `mcp-server/README.md`

- [ ] **Step 1: README 도구 표 2곳 갱신** (영문 ~L138, 한글 ~L330의 "Available Tools" 표)

각 표에 5개 행 추가(기존 컬럼 형식 `| Tool | Description | Example Prompt |`에 맞춰):
- `ttobak_list_accounts` — 내 Account 목록 — "내 어카운트 목록 보여줘"
- `ttobak_get_account` — Account 상세/멤버 — "하나은행 어카운트 정보"
- `ttobak_get_account_meetings` — 공유 미팅 목록 — "하나은행 공유 미팅 목록"
- `ttobak_get_account_insights` — 기간·유형별 인사이트 — "하나은행 5월 리스크/기회 인사이트"
- `ttobak_get_account_brief` — 묶음 원재료 — "하나은행 분기 브리프 한 번에"

- [ ] **Step 2: `/mcp` 도구 목록 블록 2곳 갱신** (영문 ~L94, 한글 ~L286)

기존 `Tools: ttobak_login, ttobak_status, ttobak_list_meetings, ttobak_get_meeting, ttobak_ask, ttobak_logout` 목록에 `ttobak_list_accounts, ttobak_get_account, ttobak_get_account_meetings, ttobak_get_account_insights, ttobak_get_account_brief`를 추가.

- [ ] **Step 3: 최종 검증 (Go + TS)**

```bash
cd /home/ec2-user/ttobak/backend && /usr/local/go/bin/go build ./... && /usr/local/go/bin/go test ./internal/...
cd /home/ec2-user/ttobak/mcp-server && npm run build
```
Expected: 모두 성공.

- [ ] **Step 4: Commit**

```bash
cd /home/ec2-user/ttobak/mcp-server && git add README.md
git commit -m "docs(mcp): document account back-data tools in README"
```

---

## CDK 메모
인프라 변경 없음. brief는 기존 서비스 합성(신규 테이블/IAM/GSI 불필요). MCP 서버는 기존 `TTOBAK_API_URL`만 사용(신규 env 없음).

## Self-Review (작성자 체크)
- **Spec 커버리지(Plan 4):** §9 MCP 도구 중 account-scoped reads — `list_accounts`/`get_account`/`get_insights`/`account_brief` + 공유미팅 목록 ✅. (`list_meetings` account 필터 강화·`export_vault`·`get/put/list_documents`는 Plan 5.) `get_meeting`/`list_meetings`는 기존 MCP에 이미 존재.
- **Placeholder:** README 갱신은 라인 근사치 + 구체 행 내용 제시(구현자가 기존 포맷에 맞춤). 코드 스텝은 전부 실제 코드.
- **타입 일관성:** Go `GetAccountBrief(ctx, userID, accountID, from, to time.Time, types []string) (*model.AccountBrief, error)`; TS `getAccountBrief(accountId, {from?,to?,types?})`. 도구명 `ttobak_get_account_brief` 등 api/index/README 일치. `AccountBrief.Account *AccountResponse` (GetAccount 반환형과 동일).
- **검증 한계:** mcp-server는 테스트 프레임워크가 없어 `npm run build` 타입체크 + 수동 호출로 검증(프레임워크 신규 도입은 YAGNI). Go쪽은 단위테스트로 커버.
- **확인 필요(구현자):** README의 정확한 라인/표 위치는 파일을 열어 확인(스킬: 근사 라인). `handler/account.go`에 `time`/`strings` import 이미 있음(Plan 3). mcp-server `npm install` 필요 여부 확인.

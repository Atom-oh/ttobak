package main

import "github.com/ttobak/backend/internal/repository"

// koreanNewsDomains is the search domain allowlist of the regulatory sources:
// the regulators themselves plus Korean outlets (and their English and
// Japanese editions, which the domain filter matches as subdomains). Measured
// 2026-11-11, the managed web index returned no dated fss.or.kr, fsc.go.kr,
// fsec.or.kr or law.go.kr pages, so coverage comes from these outlets; without
// the allowlist, Korean-name queries returned Japanese FSA pages and stock
// quote aggregators that the article reader rejects (HTTP 403, script shells).
var koreanNewsDomains = []string{
	"fss.or.kr", "fsc.go.kr", "fsec.or.kr", "law.go.kr", "moleg.go.kr", "korea.kr",
	"yna.co.kr", "koreaherald.com", "koreatimes.co.kr", "koreajoongangdaily.joins.com",
	"joongang.co.kr", "chosun.com", "donga.com", "hani.co.kr", "khan.co.kr",
	"hankookilbo.com", "hankyung.com", "kedglobal.com", "mk.co.kr", "sedaily.com",
	"fnnews.com", "edaily.co.kr", "mt.co.kr", "news1.kr", "newsis.com", "ajupress.com",
	"businesskorea.co.kr", "theinvestor.co.kr", "thebell.co.kr", "fntimes.com",
	"etnews.com", "zdnet.co.kr", "ddaily.co.kr", "bloter.net", "boannews.com",
	"dailysecu.com", "kbs.co.kr", "ytn.co.kr",
}

// regulatorySources are the operator-curated Korean financial-regulation news
// sources. English aliases matter: the index matches English coverage of
// Korean regulators far better than Korean names. Each source expands to at
// most 12 queries (news_pipeline.expand_queries).
var regulatorySources = []repository.OperatorNewsSource{
	{
		SourceID: "fss-kr", SourceName: "금융감독원",
		Aliases:       []string{"Financial Supervisory Service"},
		NewsQueries:   []string{"감독규정", "검사", "제재", "guideline"},
		SearchDomains: koreanNewsDomains,
	},
	{
		SourceID: "fsc-kr", SourceName: "금융위원회",
		Aliases:       []string{"Financial Services Commission"},
		NewsQueries:   []string{"시행령", "개정안", "보도자료", "policy"},
		SearchDomains: koreanNewsDomains,
	},
	{
		SourceID: "fsec-kr", SourceName: "금융보안원",
		Aliases:       []string{"Financial Security Institute"},
		NewsQueries:   []string{"보안", "가이드라인", "security", "AI"},
		SearchDomains: koreanNewsDomains,
	},
	{
		SourceID: "kr-finance-law", SourceName: "금융 법령 개정",
		Aliases:       []string{"enforcement decree", "시행령", "시행규칙", "감독규정"},
		NewsQueries:   []string{"금융", "financial"},
		SearchDomains: koreanNewsDomains,
	},
}

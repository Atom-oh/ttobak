package service

// CrawlerRepo is the exported version of crawlerRepo for use in external test packages.
type CrawlerRepo = crawlerRepo

// NewCrawlerServiceWithRepo creates a CrawlerService with the given repo implementation.
// Intended for testing in external packages (e.g. handler tests).
func NewCrawlerServiceWithRepo(repo CrawlerRepo) *CrawlerService {
	return &CrawlerService{repo: repo}
}

// NewInsightsServiceWithRepo creates an InsightsService with the given repo implementation.
// Intended for testing in external packages (e.g. handler tests).
func NewInsightsServiceWithRepo(repo CrawlerRepo) *InsightsService {
	return &InsightsService{repo: repo}
}

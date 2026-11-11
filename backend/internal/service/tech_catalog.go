package service

import "strings"

// techEntry is one canonical technology of the focus graph. Aliases are
// lowercase meeting tags (extractTags output) that name it exactly.
type techEntry struct {
	ID      string
	Label   string
	Group   string
	Aliases []string
}

// Technology groups, used by the web for color.
const (
	techGroupContainers = "containers"
	techGroupCompute    = "compute"
	techGroupAI         = "ai"
	techGroupDatabase   = "database"
	techGroupAnalytics  = "analytics"
	techGroupMigration  = "migration"
	techGroupPlatform   = "platform"
)

// techCatalog is the checked-in technology catalog. Matching is exact after
// lowercasing and trimming; it never guesses.
var techCatalog = []techEntry{
	{"eks", "EKS", techGroupContainers, []string{"eks", "kubernetes", "k8s", "karpenter"}},
	{"ecs", "ECS", techGroupContainers, []string{"ecs", "fargate"}},
	{"lambda", "Lambda", techGroupCompute, []string{"lambda", "serverless"}},
	{"ec2", "EC2", techGroupCompute, []string{"ec2"}},
	{"bedrock", "Bedrock", techGroupAI, []string{"bedrock"}},
	{"agentcore", "AgentCore", techGroupAI, []string{"agentcore", "bedrock-agentcore"}},
	{"sagemaker", "SageMaker", techGroupAI, []string{"sagemaker"}},
	{"genai", "GenAI", techGroupAI, []string{"genai", "llm", "rag", "ai-agent", "agent"}},
	{"oracle", "Oracle", techGroupDatabase, []string{"oracle", "oracle-db", "exadata"}},
	{"aurora", "Aurora/RDS", techGroupDatabase, []string{"aurora", "rds", "postgresql", "mysql"}},
	{"dynamodb", "DynamoDB", techGroupDatabase, []string{"dynamodb"}},
	{"redshift", "Redshift", techGroupAnalytics, []string{"redshift"}},
	{"opensearch", "OpenSearch", techGroupAnalytics, []string{"opensearch", "elasticsearch"}},
	{"msk", "MSK/Kafka", techGroupAnalytics, []string{"msk", "kafka"}},
	{"s3", "S3", techGroupAnalytics, []string{"s3", "data-lake", "datalake"}},
	{"migration", "Migration", techGroupMigration, []string{"migration", "dms", "map"}},
	{"security", "Security", techGroupPlatform, []string{"security", "iam", "waf"}},
	{"network", "Network", techGroupPlatform, []string{"network", "networking", "vpc", "dmz"}},
	{"observability", "Observability", techGroupPlatform, []string{"observability", "monitoring", "cloudwatch"}},
	{"cost", "Cost", techGroupPlatform, []string{"cost", "finops", "billing"}},
}

var techAliasIndex = func() map[string]int {
	index := map[string]int{}
	for i, entry := range techCatalog {
		for _, alias := range entry.Aliases {
			index[alias] = i
		}
	}
	return index
}()

// techByID returns the catalog entry with the given ID.
func techByID(id string) (techEntry, bool) {
	for _, entry := range techCatalog {
		if entry.ID == id {
			return entry, true
		}
	}
	return techEntry{}, false
}

// matchTechTags maps meeting tags to catalog IDs in catalog order. Tags that
// match no alias are returned, normalized and deduplicated, in other. The
// generic "general" placeholder of extractTags is dropped.
func matchTechTags(tags []string) (techIDs []string, other []string) {
	hit := make([]bool, len(techCatalog))
	seenOther := map[string]bool{}
	for _, tag := range tags {
		normalized := normalizeTechAlias(tag)
		if normalized == "" || normalized == "general" {
			continue
		}
		if i, ok := techAliasIndex[normalized]; ok {
			hit[i] = true
			continue
		}
		if !seenOther[normalized] {
			seenOther[normalized] = true
			other = append(other, normalized)
		}
	}
	for i, ok := range hit {
		if ok {
			techIDs = append(techIDs, techCatalog[i].ID)
		}
	}
	return techIDs, other
}

func normalizeTechAlias(tag string) string { return strings.ToLower(strings.TrimSpace(tag)) }

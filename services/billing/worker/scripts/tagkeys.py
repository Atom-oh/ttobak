"""Tag keys detected from an account's COH resources (no customer-specific key names).

rank(recs) orders keys by how many resources carry them. The grouping keys (service naming precedence, tag
breakdowns) are the ranked keys playing a known role (cluster, service, project, team, ...), found by what the
key is called, not by its namespace; an account with none of those uses its six most used keys instead.
Records are dicts with their tags under "tags" or "_tags".
"""
import collections, re

TOP = 6
KEEP_AWS = "aws:eks:cluster-name"
TEAM_WORDS = ("team", "owner", "squad", "department", "조직")
# a word of the key, not a fragment of another word: "team", "org/team" yes; "steam", "ownerReferences" no
TEAM_RE = re.compile(r"(?i)(?<![a-z])(%s)(?![a-z])" % "|".join(TEAM_WORDS))
MANAGED_NS = ("kubernetes.io/", "k8s.io/")  # controller-owned labels, not the customer's own tags
MANAGED_VALUES = {"owned", "shared"}
CREATED_RE = re.compile(r"(?i)created[-_]?(at|date|on)")


def _tags(r):
    t = r["_tags"] if "_tags" in r else r.get("tags")
    return t if isinstance(t, dict) else {}


def rank(recs):
    """Tag keys by number of resources carrying them (desc), then name; `aws:` keys dropped except the EKS cluster name."""
    cnt = collections.Counter(k for r in recs for k in _tags(r) if not k.startswith("aws:") or k == KEEP_AWS)
    return [k for k, _ in sorted(cnt.items(), key=lambda x: (-x[1], x[0]))]


def team_key(keys):
    """The team-like key: "team" before "owner", "squad", "department", "조직"; ties in the order given (rank order)."""
    keys = list(keys)
    for w in TEAM_WORDS:
        k = next((k for k in keys if re.search(r"(?i)(?<![a-z])%s(?![a-z])" % w, k)), None)
        if k:
            return k
    return None


def created_key(keys):
    return next((k for k in keys if CREATED_RE.search(k)), None)


ROLES = {
    "cluster": re.compile(r"(?i)(cluster[-_]?name|^cluster$|kubernetescluster)"),
    "cluster_name": re.compile(r"(?i)cluster-name"),
    "service": re.compile(r"(?i)(?<![a-z])(service|workload|app)(?![a-z])"),
    "project": re.compile(r"(?i)project"),
    "team": re.compile(r"(?i)team"),
    "team_ns": re.compile(r"(?i)/[^/]*team"),
    "team_plain": re.compile(r"(?i)^[^/]*team[^/]*$"),
    "department": re.compile(r"(?i)department"),
    "principal": re.compile(r"(?i)principal"),
    "company": re.compile(r"(?i)company"),
    "map": re.compile(r"(?i)^map-migrated$"),
    "name": re.compile(r"(?i)^name$"),
}
# role order = priority; most specific workload identifier first
PRECEDENCE_ROLES = ("cluster", "service", "project", "team", "company", "name")
DIM_ROLES = ("cluster", "team", "project", "principal", "department", "company", "map")
# a namespaced team key (org/team) outranks a plain "team"; the plain one follows the project key
TAG_KEY_ROLES = ("company", "team_ns", "department", "cluster_name", "project", "team_plain", "map")


def top(recs, n=TOP):
    return _own_ranked(recs)[:n]


def _managed(recs):
    """Keys that are controller labels: a kubernetes.io/ or k8s.io/ namespace, or only ever owned/shared."""
    vals = collections.defaultdict(set)
    for r in recs:
        for k, v in _tags(r).items():
            vals[k].add(str(v).strip().lower())
    return {k for k, v in vals.items() if any(n in k for n in MANAGED_NS) or v <= MANAGED_VALUES}


def _own_ranked(recs):
    bad = _managed(recs)
    return [k for k in rank(recs) if k not in bad]


def role_key(recs, role):
    """The most used key playing `role` (None if absent)."""
    return next((k for k in _own_ranked(recs) if ROLES[role].search(k)), None)


def _by_roles(recs, roles, per_role=None):
    ranked = _own_ranked(recs)
    out = []
    for role in roles:
        out += [k for k in [k for k in ranked if ROLES[role].search(k)][:per_role] if k not in out]
    return out


def precedence(recs):
    """Service naming: the first of these keys a resource carries names its service."""
    return _by_roles(recs, PRECEDENCE_ROLES) or top(recs)


def dims(recs):
    """Tag breakdown dimensions: the role keys plus the most used keys."""
    out = _by_roles(recs, DIM_ROLES, 2)
    return out + [k for k in top(recs) if k not in out]


def tag_keys(recs):
    """Keys of the Bedrock/COH tag attribution tables: the most used key of each role."""
    return _by_roles(recs, TAG_KEY_ROLES, 1) or top(recs)


# tag-signal keys: what names a service or workload first, then who owns it
SIGNAL_ROLES = ("service", "project", "cluster", "team", "department", "company")


def signal_keys(ranked, values=None, n=4):
    """Tag-signal keys from key names already ranked by the caller (e.g. by CUR cost), not from resource records.
    values: {key: set of lower-cased values seen}, for the owned/shared controller-label test. Falls back to the
    top n own keys when none plays a role."""
    vals = values or {}
    own = [k for k in ranked if (not k.startswith("aws:") or k == KEEP_AWS) and not any(m in k for m in MANAGED_NS)
           and not (vals.get(k) and vals[k] <= MANAGED_VALUES)]
    out = []
    for role in SIGNAL_ROLES:
        out += [k for k in own if ROLES[role].search(k) and k not in out]
    return (out or own)[:n]


def pick(tags, keys):
    """The first of `keys` present in `tags` (per-resource lookup)."""
    return next((k for k in keys if k in tags), None)

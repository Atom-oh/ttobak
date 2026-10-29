package service

import (
	"context"
	"os"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const accountDescendantReadsEnv = "ACCOUNT_DESCENDANT_READS_ENABLED"

type accountReadMembershipRepo interface {
	GetMember(context.Context, string, string) (*model.AccountMember, error)
}

type accountReadHierarchyRepo interface {
	GetAccount(context.Context, string) (*model.Account, error)
}

// A read grant is request-scoped. It never creates a child membership or
// substitutes for an exact direct-membership check on a mutation.
type accountReadGrant struct {
	AccountID              string
	Member                 *model.AccountMember
	InheritedFromAccountID string
	Path                   []repository.AccountParentLink
}

func (g *accountReadGrant) inherited() bool { return g != nil && g.InheritedFromAccountID != "" }

func (g *accountReadGrant) permission() string {
	if g.inherited() {
		return model.PermissionRead
	}
	return model.PermissionEdit
}

func (g *accountReadGrant) privacyMember() *model.AccountMember {
	if g == nil {
		return nil
	}
	if !g.inherited() {
		return g.Member
	}
	member := *g.Member
	// Inheritance is a new child grant, regardless of the ancestor's legacy
	// eligibility or the staged legacy-reader flag.
	member.SummaryPrivacyVersion = model.SummaryPrivacyVersion
	return &member
}

func (g *accountReadGrant) fingerprint() string {
	if g == nil {
		return ""
	}
	return readingHash(g)
}

func descendantReadsEnabled() bool { return os.Getenv(accountDescendantReadsEnv) == "1" }

func canonicalReadAccount(account *model.Account, id string) bool {
	return account != nil && account.AccountID == id && account.PK == model.PrefixAccount+id &&
		account.SK == model.SKAccountMeta && account.EntityType == model.EntityTypeAccount
}

func canonicalReadMember(member *model.AccountMember, id, user string) bool {
	return member != nil && member.AccountID == id && member.UserID == user &&
		member.PK == model.PrefixAccount+id && member.SK == model.PrefixMember+user &&
		member.EntityType == model.EntityTypeAccountMember
}

func resolveAccountReadGrant(ctx context.Context, repo accountReadMembershipRepo, accountID, userID string) (*accountReadGrant, error) {
	if !readingID.MatchString(accountID) || !readingID.MatchString(userID) {
		return nil, nil
	}
	member, err := repo.GetMember(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	if member != nil {
		// GetMember is the existing exact canonical-key primitive. Keep
		// direct callers independent of ancestry and its feature rollout.
		return &accountReadGrant{AccountID: accountID, Member: member}, nil
	}
	if !descendantReadsEnabled() {
		return nil, nil
	}
	hierarchy, ok := repo.(accountReadHierarchyRepo)
	if !ok {
		// Narrow legacy repositories have no hierarchy capability.
		return nil, nil
	}
	seen := make(map[string]bool)
	grant := &accountReadGrant{AccountID: accountID}
	for id := accountID; id != ""; {
		if seen[id] || len(grant.Path) > repository.AccountHierarchyMaxAncestors || !readingID.MatchString(id) {
			return nil, nil
		}
		seen[id] = true
		account, err := hierarchy.GetAccount(ctx, id)
		if err != nil {
			return nil, err
		}
		if !canonicalReadAccount(account, id) {
			return nil, nil
		}
		grant.Path = append(grant.Path, repository.AccountParentLink{AccountID: id, ParentAccountID: account.ParentAccountID})
		if id != accountID && grant.Member == nil {
			member, err := repo.GetMember(ctx, id, userID)
			if err != nil {
				return nil, err
			}
			if member != nil {
				if !canonicalReadMember(member, id, userID) {
					return nil, nil
				}
				grant.Member, grant.InheritedFromAccountID = member, id
			}
		}
		id = account.ParentAccountID
	}
	if grant.Member == nil {
		return nil, nil
	}
	return grant, nil
}

func revalidateAccountReadGrant(ctx context.Context, repo accountReadMembershipRepo, grant *accountReadGrant, userID string) (bool, error) {
	if grant == nil {
		return false, nil
	}
	current, err := resolveAccountReadGrant(ctx, repo, grant.AccountID, userID)
	return current != nil && current.fingerprint() == grant.fingerprint(), err
}

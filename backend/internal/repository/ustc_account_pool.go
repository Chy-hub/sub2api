package repository

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ListUSTCAccountPool keeps cooling members in this branch's dedicated pool.
// Transient eligibility is evaluated at selection, so recovery does not depend
// on rebuilding the general scheduler's candidate snapshot.
func (r *accountRepository) ListUSTCAccountPool(ctx context.Context, groupID *int64, includeGrouped bool) ([]service.Account, error) {
	accounts, err := r.ListModelAvailabilityCandidates(ctx, groupID, []string{service.PlatformOpenAI}, includeGrouped)
	if err != nil {
		return nil, err
	}
	pool := make([]service.Account, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if account.IsOpenAI() && account.Type == service.AccountTypeAPIKey && account.SupportsUserInfoQuota() {
			pool = append(pool, *account)
		}
	}
	return pool, nil
}

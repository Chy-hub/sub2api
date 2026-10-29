package workbuddy

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

// Checkin uses the same regional workflows as manual and scheduled tasks.
// Global rewards arrive on the following day; sending the activity beacon
// must not be presented as an immediate credit grant.
func (p *Provider) Checkin(ctx context.Context, acc *reverse.Account) (*reverse.CheckinResult, error) {
	if acc == nil {
		return nil, fmt.Errorf("workbuddy: account is required")
	}
	cred := newCredential(acc)
	if !cred.region.isGlobal() && cred.enterpriseID != "" {
		return &reverse.CheckinResult{Message: "企业账号不支持签到：企业额度按月度周期自动回满", Details: map[string]any{"status": "unsupported"}}, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, shortCallTimeout)
	defer cancel()
	client := p.tasks(acc).client(callCtx)
	var result *CheckinResult
	var err error
	if cred.region.isGlobal() {
		if cred.uid == "" {
			return nil, fmt.Errorf("workbuddy: global activity rewards require this account's uid")
		}
		result, err = client.GlobalDailyBonus(callCtx, cred)
	} else {
		result, err = client.DailyCheckin(cred)
	}
	if err != nil {
		return nil, err
	}
	message := "daily check-in completed"
	if result.Already {
		message = "already checked in today"
	} else if cred.region.isGlobal() {
		message = "daily activity beacon sent; bonus credits arrive the next day"
	}
	reward := ""
	if result.Credit > 0 {
		reward = fmt.Sprintf("%d credits", result.Credit)
	}
	return &reverse.CheckinResult{Success: true, Message: message, Reward: reward, Date: time.Now().In(cstZone).Format("2006-01-02"), Details: map[string]any{"already": result.Already, "credit": result.Credit, "energy": result.Energy, "streak": result.Streak}}, nil
}

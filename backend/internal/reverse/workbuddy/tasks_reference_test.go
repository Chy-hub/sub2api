package workbuddy

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

// Regression cases ported from CLIProxyAPI-freetier2api-plugin (MIT).
func TestParseGrowthHeatmapCells(t *testing.T) {
	data := json.RawMessage(`{"cells":[
		{"date":"2026-10-16","score":0},
		{"date":"2026-10-17","score":30},
		{"date":"2026-10-18","score":0}
	]}`)
	today, lit, err := parseTaskGrowthHeatmap(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if today.Date != "2026-10-18" || today.Score != 0 {
		t.Fatalf("today = %+v, want last cell 2026-10-18/0", today)
	}
	if len(lit) != 1 || lit[0] != "2026-10-17" {
		t.Fatalf("lit = %v, want [2026-10-17]", lit)
	}
}

func TestParseGrowthHeatmapByDate(t *testing.T) {
	data := json.RawMessage(`{"2026-10-16":{"score":0},"2026-10-18":{"score":5},"2026-10-17":{"score":30}}`)
	today, lit, err := parseTaskGrowthHeatmap(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if today.Date != "2026-10-18" || today.Score != 5 {
		t.Fatalf("today = %+v, want 2026-10-18/5", today)
	}
	if len(lit) != 2 || lit[0] != "2026-10-17" || lit[1] != "2026-10-18" {
		t.Fatalf("lit = %v, want sorted [2026-10-17 2026-10-18]", lit)
	}
}

func TestParseGrowthHeatmapEmpty(t *testing.T) {
	if _, _, err := parseTaskGrowthHeatmap(json.RawMessage(`{}`)); err == nil {
		t.Fatal("empty heatmap must error")
	}
	if _, _, err := parseTaskGrowthHeatmap(json.RawMessage(`{"cells":[]}`)); err == nil {
		t.Fatal("empty cells must error")
	}
}

func TestParseClaimResultRejectsMalformedPayloads(t *testing.T) {
	for _, raw := range []string{
		`{`,
		`<html><body>upstream error</body></html>`,
		`{"unexpected":true}`,
		`null`,
	} {
		t.Run(raw, func(t *testing.T) {
			credit, energy, errParse := parseClaimResult(json.RawMessage(raw))
			if errParse == nil {
				t.Fatalf("parseClaimResult(%q) returned nil error with %d/%d reward", raw, credit, energy)
			}
			if credit != 0 || energy != 0 {
				t.Fatalf("malformed claim result returned reward %d/%d", credit, energy)
			}
		})
	}
}

func TestParseClaimResult(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		raw          string
		wantCredit   int64
		wantEnergy   int64
		wantNilError bool
	}{
		{"reward", `{"credit":12,"energy":3}`, 12, 3, true},
		{"already claimed", `{"already_claimed":true}`, 0, 0, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			credit, energy, errParse := parseClaimResult(json.RawMessage(testCase.raw))
			if (errParse == nil) != testCase.wantNilError {
				t.Fatalf("error = %v, want nil=%v", errParse, testCase.wantNilError)
			}
			if credit != testCase.wantCredit || energy != testCase.wantEnergy {
				t.Fatalf("reward = %d/%d, want %d/%d", credit, energy, testCase.wantCredit, testCase.wantEnergy)
			}
		})
	}
}

func TestTaskSpecsForGlobalRegistersDailyBonus(t *testing.T) {
	specs := TaskSpecsFor(RegionGlobal)
	if len(specs) != 1 {
		t.Fatalf("global specs = %d, want 1", len(specs))
	}
	spec := specs[0]
	if spec.Kind != TaskKindDailyBonus || spec.Route != "/dailybonus" || !spec.Manual {
		t.Fatalf("spec = %+v", spec)
	}
	if len(spec.DefaultHours) != len(globalDailyBonusHours) {
		t.Fatalf("default hours = %v, want %v", spec.DefaultHours, globalDailyBonusHours)
	}
	// CN 不注册该族（CN 走 checkin 族）。
	for _, cnSpec := range TaskSpecsFor(RegionCN) {
		if cnSpec.Kind == TaskKindDailyBonus {
			t.Fatal("CN must not register the daily bonus family")
		}
	}
}

func TestInNightWindow(t *testing.T) {
	cases := []struct {
		hour int
		want bool
	}{
		{0, true}, {7, true}, {8, false}, {12, false},
		{22, false}, {23, true},
	}
	for _, testCase := range cases {
		now := time.Date(2026, 9, 26, testCase.hour, 30, 0, 0, cstZone)
		if got := InNightWindow(now); got != testCase.want {
			t.Fatalf("InNightWindow(%02d:30) = %v, want %v", testCase.hour, got, testCase.want)
		}
	}
}

func TestParseBalanceMatchesUpstreamShape(t *testing.T) {
	raw := `{"Response":{"Data":{"Accounts":[
	  {"PackageName":"基础套餐","CycleCapacitySize":1000,"CycleCapacityRemain":400,"CycleCapacityUsed":600},
	  {"PackageName":"加油包","CapacitySize":500,"CapacityRemain":500}
	]}}}`
	var payload billingResourceResponse
	if errUnmarshal := json.Unmarshal([]byte(raw), &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal: %v", errUnmarshal)
	}
	balance := parseBalance(payload)

	// Cycle 套餐：1000 总量、400 剩余；加油包：500/500。合计 1500 / 900。
	if balance.Total != 1500 {
		t.Fatalf("total = %v, want 1500", balance.Total)
	}
	if balance.Remain != 900 {
		t.Fatalf("remain = %v, want 900", balance.Remain)
	}
	if len(balance.Packages) != 2 {
		t.Fatalf("packages = %d, want 2", len(balance.Packages))
	}
	if balance.Packages[0].Name != "基础套餐" || balance.Packages[0].Remain != 400 {
		t.Fatalf("first package = %+v", balance.Packages[0])
	}
}

func TestPackageRemainUsedCycleWins(t *testing.T) {
	// CycleUsed 大于 size-remain 时，应以 CycleUsed 为准反推 remain。
	remain, used, size := packageRemainUsed(billingResourceAccount{
		CycleCapacitySize: 1000, CycleCapacityRemain: 900, CycleCapacityUsed: 300,
	})
	if size != 1000 || used != 300 || remain != 700 {
		t.Fatalf("cycle rule: remain=%d used=%d size=%d, want 700/300/1000", remain, used, size)
	}
	// 无 Cycle 期时回落 Capacity 三字段。
	remain, used, size = packageRemainUsed(billingResourceAccount{
		CapacitySize: 500, CapacityRemain: 200,
	})
	if size != 500 || used != 300 || remain != 200 {
		t.Fatalf("capacity fallback: remain=%d used=%d size=%d, want 200/300/500", remain, used, size)
	}
	// remain 超出 size 时钳到 size（上游偶发脏数据）。
	remain, _, size = packageRemainUsed(billingResourceAccount{
		CycleCapacitySize: 100, CycleCapacityRemain: 999,
	})
	if remain != 100 || size != 100 {
		t.Fatalf("clamp: remain=%d size=%d, want 100/100", remain, size)
	}
}

// claimHost 是只回一种上游响应的假宿主：任务列表 + 领奖。
type claimHost struct {
	taskBody  string
	claimBody string
}

func TestParseEnterpriseBalanceKeepsFractionalCredit(t *testing.T) {
	balance, errParse := parseEnterpriseBalance(json.RawMessage(`{
	  "credit":4.11,"limitNum":2000,
	  "cycleStartTime":"2026-10-01 00:00:00",
	  "cycleEndTime":"2026-10-31 23:59:59",
	  "cycleResetTime":"2026-11-01 00:00:00"}`))
	if errParse != nil {
		t.Fatalf("parse: %v", errParse)
	}
	if !balance.Enterprise || balance.Total != 2000 {
		t.Fatalf("balance = %+v", balance)
	}
	if math.Abs(balance.Remain-1995.89) > 0.0001 {
		t.Fatalf("remain = %v, want 1995.89", balance.Remain)
	}
	if len(balance.Packages) != 1 {
		t.Fatalf("packages = %d, want 1", len(balance.Packages))
	}
	pkg := balance.Packages[0]
	if pkg.Name != "企业额度" {
		t.Fatalf("package name = %q", pkg.Name)
	}
	if pkg.ExpireAt != 0 {
		t.Fatal("企业额度到点回满，不能设 ExpireAt（会被显示成即将过期）")
	}
	if pkg.ResetAt == 0 {
		t.Fatal("企业额度必须带周期回满时刻")
	}
	if !strings.Contains(pkg.Period, "2026-10-01") || !strings.Contains(pkg.Period, "2026-10-31") {
		t.Fatalf("period = %q", pkg.Period)
	}
}

func TestParseEnterpriseBalanceRejectsMissingLimit(t *testing.T) {
	for _, raw := range []string{`{}`, `{"credit":1}`, `null`, `<html>error</html>`} {
		if _, errParse := parseEnterpriseBalance(json.RawMessage(raw)); errParse == nil {
			t.Fatalf("parseEnterpriseBalance(%s) 必须报错", raw)
		}
	}
}

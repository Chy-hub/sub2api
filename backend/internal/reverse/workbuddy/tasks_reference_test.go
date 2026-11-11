package workbuddy

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
	"github.com/stretchr/testify/require"
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

func TestDailyBonusBeaconCarriesTheFullChatRequestSendFieldSet(t *testing.T) {
	// The upstream validates events by type and rejects the *whole batch* with
	// 400 "all events rejected by format validation" when one required field is
	// missing. The daily bonus beacon once shipped only 16 of the 31 fields, so
	// global accounts silently never earned the reward.
	//
	// Both builders emit the same event type, so their field sets must match.
	// Compare the emitted payloads rather than hard-coding a list: a field added
	// to one builder then fails here instead of quietly failing upstream.
	cred := testCredential("global")
	beacon := DailyBonusBeaconEvent(cred)

	var captured map[string]any
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/report" {
			_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		// /v2/report takes a bare array of events.
		var events []map[string]any
		if errUnmarshal := json.Unmarshal(raw, &events); errUnmarshal != nil {
			t.Errorf("decode report body: %v", errUnmarshal)
			return
		}
		if captured == nil && len(events) > 0 {
			captured = events[0]
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
	}
	p := localProvider(t, handler)
	acc := &reverse.Account{Region: "global", Credentials: map[string]any{"uid": "u1", "access_token": "access"}}
	adapter := p.tasks(acc)
	native, errNative := adapter.Native(acc)
	require.NoError(t, errNative)
	require.NoError(t, adapter.client(context.Background()).ReportChatActivity(context.Background(), native, "conv", "req", "", ""))
	require.NotNil(t, captured, "ReportChatActivity did not reach the upstream")

	for field := range captured {
		if _, ok := beacon[field]; !ok {
			t.Errorf("beacon is missing field %q that ReportChatActivity sends", field)
		}
	}
	for field := range beacon {
		if _, ok := captured[field]; !ok {
			t.Errorf("beacon sends field %q that ReportChatActivity does not", field)
		}
	}
}

// TestWaitGrowthClaimableQueriesBeforeSleeping locks the scheduling detail that
// dominated the wall-clock cost of a full task run: the task is usually
// claimable the moment the action returns, and sleeping first added 3–9 seconds
// per task for nothing.
//
// It also locks the opposite mistake: a nil task must not be treated as "no
// reward", because the upstream list is eventually consistent and a task that
// was just completed may not appear yet.
func TestWaitGrowthClaimableQueriesBeforeSleeping(t *testing.T) {
	t.Run("already claimable: one list call, no sleep", func(t *testing.T) {
		var calls int
		p := localProvider(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != growthTasksPath {
				_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
				return
			}
			calls++
			_, _ = io.WriteString(w, `{"tasks":[{"task_code":"alpha","current":5,"target":5}]}`)
		})
		acc := &reverse.Account{Region: "cn", Credentials: map[string]any{"uid": "u1"}}
		adapter := p.tasks(acc)
		native, errNative := adapter.Native(acc)

		start := time.Now()
		got := waitGrowthClaimable(context.Background(), adapter.client(context.Background()), native, "alpha")
		require.NoError(t, errNative)
		require.True(t, got)
		require.Equal(t, 1, calls, "a claimable task must be detected on the first query")
		require.Less(t, time.Since(start), time.Second, "no sleep should happen when the first query succeeds")
	})

	t.Run("task not listed yet: keeps polling instead of giving up", func(t *testing.T) {
		var calls int
		p := localProvider(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != growthTasksPath {
				_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
				return
			}
			calls++
			// The task only surfaces on the second query, mirroring upstream's
			// eventual consistency right after an action completes.
			if calls < 2 {
				_, _ = io.WriteString(w, `{"tasks":[]}`)
				return
			}
			_, _ = io.WriteString(w, `{"tasks":[{"task_code":"alpha","current":3,"target":3}]}`)
		})
		acc := &reverse.Account{Region: "cn", Credentials: map[string]any{"uid": "u1"}}
		adapter := p.tasks(acc)
		native, errNative := adapter.Native(acc)

		got := waitGrowthClaimable(context.Background(), adapter.client(context.Background()), native, "alpha")
		require.NoError(t, errNative)
		require.True(t, got, "a task that appears on retry must still be claimed")
		require.Equal(t, 2, calls)
	})

	t.Run("already claimed: terminal, no further polling", func(t *testing.T) {
		var calls int
		p := localProvider(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != growthTasksPath {
				_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
				return
			}
			calls++
			_, _ = io.WriteString(w, `{"tasks":[{"task_code":"alpha","current":5,"target":5,"claimed":true}]}`)
		})
		acc := &reverse.Account{Region: "cn", Credentials: map[string]any{"uid": "u1"}}
		adapter := p.tasks(acc)
		native, errNative := adapter.Native(acc)

		got := waitGrowthClaimable(context.Background(), adapter.client(context.Background()), native, "alpha")
		require.NoError(t, errNative)
		require.False(t, got)
		require.Equal(t, 1, calls)
	})
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

func TestHeatmapLitOnMatchesExactDate(t *testing.T) {
	lit := []string{"2026-11-06", "2026-11-07"}
	if !heatmapLitOn(lit, "2026-11-07") {
		t.Fatal("an exact date match must report lit")
	}
	if heatmapLitOn(lit, "2026-11-08") {
		t.Fatal("a missing date must not report lit")
	}
}

// TestGlobalDailyBonusUsesUpstreamStreak locks the streak source: the day count
// comes from the upstream streak endpoint, never from counting lit heatmap
// entries (which only ever grows and diverges on break/makeup days).
func TestGlobalDailyBonusUsesUpstreamStreak(t *testing.T) {
	today := time.Now().In(cstZone).Format("2006-01-02")
	yesterday := time.Now().In(cstZone).AddDate(0, 0, -1).Format("2006-01-02")

	cases := []struct {
		name       string
		heatmap    string
		streakBody string
		streakFail bool
		wantStreak int64
		wantReport bool
	}{
		{
			name:       "尚未点亮：透传上游天数并上报信标",
			heatmap:    `{"code":0,"data":{"cells":[{"date":"` + yesterday + `","score":1}]}}`,
			streakBody: `{"code":0,"data":{"streak":{"days":12}}}`,
			wantStreak: 12,
			wantReport: true,
		},
		{
			name:       "今日已点亮：不重复上报",
			heatmap:    `{"code":0,"data":{"cells":[{"date":"` + today + `","score":1}]}}`,
			streakBody: `{"code":0,"data":{"streak":{"days":7}}}`,
			wantStreak: 7,
			wantReport: false,
		},
		{
			name:       "连登接口失败：返回 0 而不是猜一个天数",
			heatmap:    `{"code":0,"data":{"cells":[{"date":"` + yesterday + `","score":1}]}}`,
			streakFail: true,
			wantStreak: 0,
			wantReport: true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			reported := false
			p := localProvider(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "heatmap"):
					_, _ = io.WriteString(w, testCase.heatmap)
				case strings.HasSuffix(r.URL.Path, "streak"):
					if testCase.streakFail {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					_, _ = io.WriteString(w, testCase.streakBody)
				case r.URL.Path == "/v2/report":
					reported = true
					_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
				default:
					_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
				}
			})
			acc := &reverse.Account{Region: "global", Credentials: map[string]any{"uid": "u1", "access_token": "access"}}
			result, err := p.Checkin(context.Background(), acc)
			require.NoError(t, err)
			require.Equal(t, testCase.wantStreak, result.Details["streak"])
			require.Equal(t, testCase.wantReport, reported)
		})
	}
}

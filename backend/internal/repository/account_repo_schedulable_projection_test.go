package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

type captureEntQueryMatcher struct {
	actual *string
}

func TestListSchedulableCapacityByGroupIDsStoresOnlyUSTCScopeHash(t *testing.T) {
	var capturedSQL string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(captureEntQueryMatcher{actual: &capturedSQL}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })
	repo := newAccountRepositoryWithSQL(client, db, nil)

	const apiKey = "secret-ustc-key"
	mock.ExpectQuery("schedulable capacity by groups").
		WillReturnRows(sqlmock.NewRows([]string{
			"group_id", "account_id", "platform", "type", "concurrency", "extra", "credentials",
			"session_window_start", "session_window_end", "session_window_status",
		}).AddRow(
			int64(7), int64(11), service.PlatformOpenAI, service.AccountTypeAPIKey, 10,
			`{"upstream_userinfo_limits_known":true}`, `{"base_url":"https://api.llm.ustc.edu.cn","api_key":"`+apiKey+`"}`,
			nil, nil, "",
		).AddRow(
			int64(7), int64(12), service.PlatformOpenAI, service.AccountTypeUpstream, 10,
			`{"upstream_userinfo_limits_known":true}`, `{"base_url":"https://api.llm.ustc.edu.cn","api_key":"upstream-key"}`,
			nil, nil, "",
		))

	rows, err := repo.ListSchedulableCapacityByGroupIDs(context.Background(), []int64{7})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, service.PlatformOpenAI, rows[0].Platform)
	require.Equal(t, service.AccountTypeAPIKey, rows[0].Type)
	require.Equal(t, service.USTCKeyScope(&service.Account{
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.llm.ustc.edu.cn",
			"api_key":  apiKey,
		},
		Extra: map[string]any{"upstream_userinfo_limits_known": true},
	}), rows[0].USTCScope)
	require.Empty(t, rows[1].USTCScope, "upstream quota accounts do not get automated API-key capacity scope")
	serialized, err := json.Marshal(rows[0])
	require.NoError(t, err)
	require.NotContains(t, string(serialized), apiKey)
	require.NoError(t, mock.ExpectationsWereMet())

	selectClause, _, found := strings.Cut(normalizeSQLWhitespace(capturedSQL), " FROM ")
	require.True(t, found, "unexpected capacity projection SQL: %s", capturedSQL)
	require.Contains(t, selectClause, "a.platform")
	require.Contains(t, selectClause, "a.type")
	require.Contains(t, selectClause, "a.credentials")
}

func (m captureEntQueryMatcher) Match(_, actual string) error {
	if m.actual == nil {
		return fmt.Errorf("query capture target is nil")
	}
	*m.actual = actual
	return nil
}

func TestListSchedulableAccountLoadsUsesSingleProjectionQuery(t *testing.T) {
	var capturedSQL string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(captureEntQueryMatcher{actual: &capturedSQL}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })
	repo := newAccountRepositoryWithSQL(client, db, nil)

	mock.ExpectQuery("schedulable account load projection").
		WillReturnRows(sqlmock.NewRows([]string{"id", "concurrency", "load_factor"}).
			AddRow(int64(11), 3, nil).
			AddRow(int64(12), 2, 7))

	loads, err := repo.ListSchedulableAccountLoads(context.Background())
	require.NoError(t, err)
	require.Len(t, loads, 2)
	require.Equal(t, int64(11), loads[0].ID)
	require.Equal(t, 3, loads[0].MaxConcurrency)
	require.Equal(t, int64(12), loads[1].ID)
	require.Equal(t, 7, loads[1].MaxConcurrency)
	require.NoError(t, mock.ExpectationsWereMet(), "projection path must execute exactly one query")

	normalized := normalizeSQLWhitespace(capturedSQL)
	selectClause, _, found := strings.Cut(normalized, " FROM ")
	require.True(t, found, "unexpected projection SQL: %s", normalized)
	require.Equal(t, 2, strings.Count(selectClause, ","), "projection must select exactly three columns: %s", selectClause)
	require.Contains(t, selectClause, `"id"`)
	require.Contains(t, selectClause, `"concurrency"`)
	require.Contains(t, selectClause, `"load_factor"`)
	require.NotContains(t, selectClause, "credentials")
	require.NotContains(t, selectClause, "extra")
	require.NotContains(t, selectClause, "proxy_id")
	require.NotContains(t, normalized, "account_groups")
	require.NotContains(t, normalized, "proxies")
	for _, predicateColumn := range []string{
		"status",
		"schedulable",
		"temp_unschedulable_until",
		"expires_at",
		"auto_pause_on_expired",
		"overload_until",
		"rate_limit_reset_at",
		"deleted_at",
	} {
		require.Contains(t, normalized, predicateColumn)
	}
	_, orderClause, hasOrder := strings.Cut(normalized, " ORDER BY ")
	require.True(t, hasOrder, "projection query must preserve schedulable account order: %s", normalized)
	require.Contains(t, orderClause, `"priority" ASC`)
}

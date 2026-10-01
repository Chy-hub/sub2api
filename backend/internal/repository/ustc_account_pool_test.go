package repository

import (
	"context"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

func TestUSTCAccountPoolRetainsCoolingMembersAndGroupScope(t *testing.T) {
	var captured string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(captureEntQueryMatcher{actual: &captured}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	repo := newAccountRepositoryWithSQL(client, db, nil)
	mock.ExpectQuery("ustc pool").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	groupID := int64(9)
	pool, err := repo.ListUSTCAccountPool(context.Background(), &groupID, false)
	require.NoError(t, err)
	require.Empty(t, pool)
	require.NoError(t, mock.ExpectationsWereMet())
	_, where, found := strings.Cut(normalizeSQLWhitespace(captured), " WHERE ")
	require.True(t, found)
	where, _, _ = strings.Cut(where, " ORDER BY ")
	for _, persistent := range []string{"group_id", "status", "schedulable", "platform"} {
		require.Contains(t, where, persistent)
	}
	for _, transient := range []string{"rate_limit_reset_at", "temp_unschedulable_until", "overload_until", "expires_at"} {
		require.NotContains(t, where, transient, "transient cooling must not remove membership until the 5min general rebuild")
	}
}

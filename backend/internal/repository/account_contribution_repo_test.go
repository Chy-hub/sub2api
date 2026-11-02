package repository

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newContributionRepoSQLMock(t *testing.T) (service.AccountContributionRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return NewAccountContributionRepository(db), mock
}

func accountTaskRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "name", "description", "enabled", "platform", "account_types", "target_group_ids",
		"reward_group_id", "reward_days", "max_per_user", "expected_base_url", "verify_models",
		"reject_on_verify_fail", "priority", "concurrency", "rate_multiplier", "sort_order",
		"created_at", "updated_at", "deleted_at",
	})
}

func accountSubmissionRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "task_id", "user_id", "account_id", "status", "verification_status",
		"verification_error", "model_count", "credential_fingerprint", "reviewed_by", "reviewed_at",
		"review_note", "reward_subscription_id", "reward_granted_at", "created_at", "updated_at",
	})
}

func TestAccountContributionRepoCreateTaskEncodesJSONColumns(t *testing.T) {
	repo, mock := newContributionRepoSQLMock(t)
	now := time.Now().UTC()

	mock.ExpectQuery("INSERT INTO account_tasks").
		WithArgs(
			"贡献任务", "", true, "openai",
			[]byte(`["apikey"]`), []byte(`[4,5]`),
			int64(9), 30, 1, "api.llm.ustc.edu.cn", false,
			true, 50, 3, nil, 0,
		).
		WillReturnRows(accountTaskRows().AddRow(
			1, "贡献任务", "", true, "openai", []byte(`["apikey"]`), []byte(`[4,5]`),
			9, 30, 1, "api.llm.ustc.edu.cn", false, true, 50, 3, nil, 0,
			now, now, nil,
		))

	task := &service.AccountTask{
		Name:               "贡献任务",
		Enabled:            true,
		Platform:           "openai",
		AccountTypes:       []string{"apikey"},
		TargetGroupIDs:     []int64{4, 5},
		RewardGroupID:      9,
		RewardDays:         30,
		MaxPerUser:         1,
		ExpectedBaseURL:    "api.llm.ustc.edu.cn",
		VerifyModels:       false,
		RejectOnVerifyFail: true,
		Priority:           50,
		Concurrency:        3,
	}
	require.NoError(t, repo.CreateTask(context.Background(), task))
	require.Equal(t, int64(1), task.ID)
	require.Equal(t, []string{"apikey"}, task.AccountTypes)
	require.Equal(t, []int64{4, 5}, task.TargetGroupIDs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountContributionRepoGetTaskHandlesNullRateMultiplier(t *testing.T) {
	repo, mock := newContributionRepoSQLMock(t)
	now := time.Now().UTC()

	mock.ExpectQuery("SELECT .* FROM account_tasks WHERE id = \\$1").
		WithArgs(int64(3)).
		WillReturnRows(accountTaskRows().AddRow(
			3, "任务", "", true, "openai", []byte(`["apikey"]`), []byte(`[]`),
			9, 30, 1, "", true, true, 50, 3, nil, 0,
			now, now, nil,
		))

	task, err := repo.GetTaskByID(context.Background(), 3)
	require.NoError(t, err)
	require.NotNil(t, task)
	require.Nil(t, task.RateMultiplier)
	// JSONB 解码为 nil 时必须归一成空切片，避免前端拿到 null。
	require.Equal(t, []int64{}, task.TargetGroupIDs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountContributionRepoGetTaskReturnsNilWhenMissing(t *testing.T) {
	repo, mock := newContributionRepoSQLMock(t)
	mock.ExpectQuery("SELECT .* FROM account_tasks WHERE id = \\$1").
		WithArgs(int64(99)).
		WillReturnRows(accountTaskRows())

	task, err := repo.GetTaskByID(context.Background(), 99)
	require.NoError(t, err)
	require.Nil(t, task)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountContributionRepoCreateSubmission(t *testing.T) {
	repo, mock := newContributionRepoSQLMock(t)
	now := time.Now().UTC()

	mock.ExpectQuery("INSERT INTO account_submissions").
		WithArgs(int64(1), int64(7), int64(42), "pending", "passed", "", 12, "fp-1").
		WillReturnRows(accountSubmissionRows().AddRow(
			5, 1, 7, 42, "pending", "passed", "", 12, "fp-1",
			nil, nil, "", nil, nil, now, now,
		))

	sub := &service.AccountSubmission{
		TaskID:                1,
		UserID:                7,
		AccountID:             42,
		Status:                "pending",
		VerificationStatus:    "passed",
		ModelCount:            12,
		CredentialFingerprint: "fp-1",
	}
	require.NoError(t, repo.CreateSubmission(context.Background(), sub))
	require.Equal(t, int64(5), sub.ID)
	require.Nil(t, sub.ReviewedBy)
	require.Nil(t, sub.RewardGrantedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountContributionRepoCountUserSubmissions(t *testing.T) {
	repo, mock := newContributionRepoSQLMock(t)
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM account_submissions").
		WithArgs(int64(1), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))

	count, err := repo.CountUserSubmissions(context.Background(), 1, 7)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountContributionRepoHasUserReward(t *testing.T) {
	repo, mock := newContributionRepoSQLMock(t)
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(int64(1), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	has, err := repo.HasUserReward(context.Background(), 1, 7)
	require.NoError(t, err)
	require.True(t, has)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountContributionRepoFindActiveByFingerprintSkipsEmpty(t *testing.T) {
	repo, mock := newContributionRepoSQLMock(t)
	// 空指纹不应触发任何查询。
	sub, err := repo.FindActiveByFingerprint(context.Background(), "")
	require.NoError(t, err)
	require.Nil(t, sub)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountContributionRepoFindActiveByFingerprint(t *testing.T) {
	repo, mock := newContributionRepoSQLMock(t)
	now := time.Now().UTC()

	mock.ExpectQuery("SELECT .* FROM account_submissions").
		WithArgs("fp-1").
		WillReturnRows(accountSubmissionRows().AddRow(
			5, 1, 7, 42, "pending", "passed", "", 12, "fp-1",
			nil, nil, "", nil, nil, now, now,
		))

	sub, err := repo.FindActiveByFingerprint(context.Background(), "fp-1")
	require.NoError(t, err)
	require.NotNil(t, sub)
	require.Equal(t, int64(5), sub.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountContributionRepoUpdateSubmissionSetsReward(t *testing.T) {
	repo, mock := newContributionRepoSQLMock(t)
	now := time.Now().UTC()
	reviewer := int64(1)
	rewardSub := int64(88)

	mock.ExpectQuery("UPDATE account_submissions SET").
		WithArgs(
			int64(5), "approved", "passed", "", 12,
			&reviewer, AnyTime{}, "ok", &rewardSub, AnyTime{},
		).
		WillReturnRows(accountSubmissionRows().AddRow(
			5, 1, 7, 42, "approved", "passed", "", 12, "fp-1",
			reviewer, now, "ok", rewardSub, now, now, now,
		))

	sub := &service.AccountSubmission{
		ID:                   5,
		Status:               "approved",
		VerificationStatus:   "passed",
		ModelCount:           12,
		ReviewedBy:           &reviewer,
		ReviewedAt:           &now,
		ReviewNote:           "ok",
		RewardSubscriptionID: &rewardSub,
		RewardGrantedAt:      &now,
	}
	require.NoError(t, repo.UpdateSubmission(context.Background(), sub))
	require.Equal(t, "approved", sub.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

// AnyTime 匹配任意时间参数。
type AnyTime struct{}

func (AnyTime) Match(driver.Value) bool { return true }

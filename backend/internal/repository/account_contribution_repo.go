package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type accountContributionRepository struct {
	sql *sql.DB
}

// NewAccountContributionRepository 创建账号贡献任务仓储。
func NewAccountContributionRepository(sqlDB *sql.DB) service.AccountContributionRepository {
	return &accountContributionRepository{sql: sqlDB}
}

const accountTaskSelectColumns = `id, name, description, enabled, platform, account_types, target_group_ids,
	reward_group_id, reward_days, max_per_user, expected_base_url, verify_models,
	reject_on_verify_fail, priority, concurrency, rate_multiplier, sort_order,
	created_at, updated_at, deleted_at`

func (r *accountContributionRepository) CreateTask(ctx context.Context, task *service.AccountTask) error {
	accountTypes, err := json.Marshal(accountTypesOrEmpty(task.AccountTypes))
	if err != nil {
		return err
	}
	targetGroups, err := json.Marshal(task.TargetGroupIDs)
	if err != nil {
		return err
	}
	row := r.sql.QueryRowContext(ctx, `
		INSERT INTO account_tasks (
			name, description, enabled, platform, account_types, target_group_ids,
			reward_group_id, reward_days, max_per_user, expected_base_url, verify_models,
			reject_on_verify_fail, priority, concurrency, rate_multiplier, sort_order
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		RETURNING `+accountTaskSelectColumns,
		task.Name, task.Description, task.Enabled, task.Platform, accountTypes, targetGroups,
		task.RewardGroupID, task.RewardDays, task.MaxPerUser, task.ExpectedBaseURL, task.VerifyModels,
		task.RejectOnVerifyFail, task.Priority, task.Concurrency, task.RateMultiplier, task.SortOrder,
	)
	created, err := scanAccountTask(row)
	if err != nil {
		return err
	}
	*task = *created
	return nil
}

func (r *accountContributionRepository) UpdateTask(ctx context.Context, task *service.AccountTask) error {
	accountTypes, err := json.Marshal(task.AccountTypes)
	if err != nil {
		return err
	}
	targetGroups, err := json.Marshal(task.TargetGroupIDs)
	if err != nil {
		return err
	}
	row := r.sql.QueryRowContext(ctx, `
		UPDATE account_tasks SET
			name = $2, description = $3, enabled = $4, platform = $5, account_types = $6,
			target_group_ids = $7, reward_group_id = $8, reward_days = $9, max_per_user = $10,
			expected_base_url = $11, verify_models = $12, reject_on_verify_fail = $13,
			priority = $14, concurrency = $15, rate_multiplier = $16, sort_order = $17,
			updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING `+accountTaskSelectColumns,
		task.ID, task.Name, task.Description, task.Enabled, task.Platform, accountTypes,
		targetGroups, task.RewardGroupID, task.RewardDays, task.MaxPerUser,
		task.ExpectedBaseURL, task.VerifyModels, task.RejectOnVerifyFail,
		task.Priority, task.Concurrency, task.RateMultiplier, task.SortOrder,
	)
	updated, err := scanAccountTask(row)
	if err != nil {
		return err
	}
	*task = *updated
	return nil
}

func (r *accountContributionRepository) GetTaskByID(ctx context.Context, id int64) (*service.AccountTask, error) {
	row := r.sql.QueryRowContext(ctx, `
		SELECT `+accountTaskSelectColumns+`
		FROM account_tasks WHERE id = $1 AND deleted_at IS NULL`, id)
	task, err := scanAccountTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return task, nil
}

// GetTaskByIDIncludingDeleted 读取任务时不排除软删除记录。
// 审核一条待审提交需要任务配置（入组、奖励），而任务被删除后这些提交仍然有效。
func (r *accountContributionRepository) GetTaskByIDIncludingDeleted(ctx context.Context, id int64) (*service.AccountTask, error) {
	row := r.sql.QueryRowContext(ctx, `
		SELECT `+accountTaskSelectColumns+`
		FROM account_tasks WHERE id = $1`, id)
	task, err := scanAccountTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return task, nil
}

func (r *accountContributionRepository) ListTasks(ctx context.Context, enabledOnly bool) ([]*service.AccountTask, error) {
	query := `
		SELECT ` + accountTaskSelectColumns + `
		FROM account_tasks WHERE deleted_at IS NULL`
	if enabledOnly {
		query += ` AND enabled = TRUE`
	}
	query += ` ORDER BY sort_order ASC, id DESC`
	rows, err := r.sql.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	tasks := make([]*service.AccountTask, 0)
	for rows.Next() {
		task, err := scanAccountTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (r *accountContributionRepository) DeleteTask(ctx context.Context, id int64) error {
	_, err := r.sql.ExecContext(ctx,
		`UPDATE account_tasks SET deleted_at = NOW(), enabled = FALSE WHERE id = $1 AND deleted_at IS NULL`, id)
	return err
}

type contributionRowScanner interface {
	Scan(dest ...any) error
}

func scanAccountTask(row contributionRowScanner) (*service.AccountTask, error) {
	var (
		task         service.AccountTask
		accountTypes []byte
		targetGroups []byte
		rateMult     sql.NullFloat64
		deletedAt    sql.NullTime
	)
	if err := row.Scan(
		&task.ID, &task.Name, &task.Description, &task.Enabled, &task.Platform, &accountTypes,
		&targetGroups, &task.RewardGroupID, &task.RewardDays, &task.MaxPerUser, &task.ExpectedBaseURL,
		&task.VerifyModels, &task.RejectOnVerifyFail, &task.Priority, &task.Concurrency, &rateMult,
		&task.SortOrder, &task.CreatedAt, &task.UpdatedAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	if len(accountTypes) > 0 {
		if err := json.Unmarshal(accountTypes, &task.AccountTypes); err != nil {
			return nil, fmt.Errorf("decode account_types: %w", err)
		}
	}
	if len(targetGroups) > 0 {
		if err := json.Unmarshal(targetGroups, &task.TargetGroupIDs); err != nil {
			return nil, fmt.Errorf("decode target_group_ids: %w", err)
		}
	}
	if rateMult.Valid {
		value := rateMult.Float64
		task.RateMultiplier = &value
	}
	if deletedAt.Valid {
		value := deletedAt.Time
		task.DeletedAt = &value
	}
	if task.AccountTypes == nil {
		task.AccountTypes = []string{}
	}
	if task.TargetGroupIDs == nil {
		task.TargetGroupIDs = []int64{}
	}
	return &task, nil
}

// accountTypesOrEmpty 保证 NULL 语义稳定，写入空数组而不是 JSON null。
func accountTypesOrEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

const accountSubmissionSelectColumns = `id, task_id, user_id, account_id, status, verification_status,
	verification_error, model_count, credential_fingerprint, reviewed_by, reviewed_at,
	review_note, reward_subscription_id, reward_granted_at, created_at, updated_at`

func (r *accountContributionRepository) CreateSubmission(ctx context.Context, sub *service.AccountSubmission) error {
	row := r.sql.QueryRowContext(ctx, `
		INSERT INTO account_submissions (
			task_id, user_id, account_id, status, verification_status, verification_error,
			model_count, credential_fingerprint
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING `+accountSubmissionSelectColumns,
		sub.TaskID, sub.UserID, sub.AccountID, sub.Status, sub.VerificationStatus,
		sub.VerificationError, sub.ModelCount, sub.CredentialFingerprint,
	)
	created, err := scanAccountSubmission(row)
	if err != nil {
		return err
	}
	*sub = *created
	return nil
}

func (r *accountContributionRepository) UpdateSubmission(ctx context.Context, sub *service.AccountSubmission) error {
	row := r.sql.QueryRowContext(ctx, `
		UPDATE account_submissions SET
			status = $2, verification_status = $3, verification_error = $4, model_count = $5,
			reviewed_by = $6, reviewed_at = $7, review_note = $8,
			reward_subscription_id = $9, reward_granted_at = $10, updated_at = NOW()
		WHERE id = $1
		RETURNING `+accountSubmissionSelectColumns,
		sub.ID, sub.Status, sub.VerificationStatus, sub.VerificationError, sub.ModelCount,
		sub.ReviewedBy, sub.ReviewedAt, sub.ReviewNote, sub.RewardSubscriptionID, sub.RewardGrantedAt,
	)
	updated, err := scanAccountSubmission(row)
	if err != nil {
		return err
	}
	*sub = *updated
	return nil
}

// UpdateSubmissionIfStatus 只在当前状态等于 fromStatus 时写入，返回是否命中。
// 审核路径用它做原子的状态迁移：并发的第二次审核会命中 0 行，不会覆盖先到的结果。
func (r *accountContributionRepository) UpdateSubmissionIfStatus(
	ctx context.Context, sub *service.AccountSubmission, fromStatus string,
) (bool, error) {
	row := r.sql.QueryRowContext(ctx, `
		UPDATE account_submissions SET
			status = $2, verification_status = $3, verification_error = $4, model_count = $5,
			reviewed_by = $6, reviewed_at = $7, review_note = $8,
			reward_subscription_id = $9, reward_granted_at = $10, updated_at = NOW()
		WHERE id = $1 AND status = $11
		RETURNING `+accountSubmissionSelectColumns,
		sub.ID, sub.Status, sub.VerificationStatus, sub.VerificationError, sub.ModelCount,
		sub.ReviewedBy, sub.ReviewedAt, sub.ReviewNote, sub.RewardSubscriptionID, sub.RewardGrantedAt,
		fromStatus,
	)
	updated, err := scanAccountSubmission(row)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	*sub = *updated
	return true, nil
}

func (r *accountContributionRepository) GetSubmissionByID(ctx context.Context, id int64) (*service.AccountSubmission, error) {
	row := r.sql.QueryRowContext(ctx,
		`SELECT `+accountSubmissionSelectColumns+` FROM account_submissions WHERE id = $1`, id)
	sub, err := scanAccountSubmission(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return sub, nil
}

func (r *accountContributionRepository) ListSubmissionsByUser(ctx context.Context, userID int64) ([]*service.AccountSubmission, error) {
	return r.querySubmissions(ctx,
		`SELECT `+accountSubmissionSelectColumns+` FROM account_submissions
		 WHERE user_id = $1 ORDER BY id DESC`, userID)
}

func (r *accountContributionRepository) ListPendingSubmissions(ctx context.Context, taskID int64) ([]*service.AccountSubmission, error) {
	if taskID > 0 {
		return r.querySubmissions(ctx,
			`SELECT `+accountSubmissionSelectColumns+` FROM account_submissions
			 WHERE status = 'pending' AND task_id = $1 ORDER BY id ASC`, taskID)
	}
	return r.querySubmissions(ctx,
		`SELECT `+accountSubmissionSelectColumns+` FROM account_submissions
		 WHERE status = 'pending' ORDER BY id ASC`)
}

func (r *accountContributionRepository) querySubmissions(ctx context.Context, query string, args ...any) ([]*service.AccountSubmission, error) {
	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make([]*service.AccountSubmission, 0)
	for rows.Next() {
		sub, err := scanAccountSubmission(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, sub)
	}
	return result, rows.Err()
}

func (r *accountContributionRepository) CountUserSubmissions(ctx context.Context, taskID, userID int64) (int, error) {
	var count int
	err := r.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM account_submissions WHERE task_id = $1 AND user_id = $2 AND status <> 'rejected'`,
		taskID, userID).Scan(&count)
	return count, err
}

func (r *accountContributionRepository) FindActiveByFingerprint(ctx context.Context, fingerprint string) (*service.AccountSubmission, error) {
	if fingerprint == "" {
		return nil, nil
	}
	row := r.sql.QueryRowContext(ctx,
		`SELECT `+accountSubmissionSelectColumns+` FROM account_submissions
		 WHERE credential_fingerprint = $1 AND status <> 'rejected' LIMIT 1`, fingerprint)
	sub, err := scanAccountSubmission(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return sub, nil
}

func (r *accountContributionRepository) HasUserReward(ctx context.Context, taskID, userID int64) (bool, error) {
	var exists bool
	err := r.sql.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM account_submissions
			WHERE task_id = $1 AND user_id = $2 AND reward_granted_at IS NOT NULL
		)`, taskID, userID).Scan(&exists)
	return exists, err
}

func scanAccountSubmission(row contributionRowScanner) (*service.AccountSubmission, error) {
	var (
		sub             service.AccountSubmission
		reviewedBy      sql.NullInt64
		reviewedAt      sql.NullTime
		rewardSubID     sql.NullInt64
		rewardGrantedAt sql.NullTime
	)
	if err := row.Scan(
		&sub.ID, &sub.TaskID, &sub.UserID, &sub.AccountID, &sub.Status, &sub.VerificationStatus,
		&sub.VerificationError, &sub.ModelCount, &sub.CredentialFingerprint, &reviewedBy, &reviewedAt,
		&sub.ReviewNote, &rewardSubID, &rewardGrantedAt, &sub.CreatedAt, &sub.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if reviewedBy.Valid {
		value := reviewedBy.Int64
		sub.ReviewedBy = &value
	}
	if reviewedAt.Valid {
		value := reviewedAt.Time
		sub.ReviewedAt = &value
	}
	if rewardSubID.Valid {
		value := rewardSubID.Int64
		sub.RewardSubscriptionID = &value
	}
	if rewardGrantedAt.Valid {
		value := rewardGrantedAt.Time
		sub.RewardGrantedAt = &value
	}
	return &sub, nil
}

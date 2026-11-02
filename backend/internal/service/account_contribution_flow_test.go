package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---- AdminService 测试替身 ----

type contributionAdminStub struct {
	AdminService

	created      []*CreateAccountInput
	updates      []adminUpdateCall
	schedulables []adminSchedulableCall
	deleted      []int64
}

type adminUpdateCall struct {
	id    int64
	input *UpdateAccountInput
}

type adminSchedulableCall struct {
	id          int64
	schedulable bool
}

func (s *contributionAdminStub) CreateAccount(_ context.Context, input *CreateAccountInput) (*Account, error) {
	s.created = append(s.created, input)
	status := StatusActive
	schedulable := true
	if input.PendingReview {
		status = "inactive"
		schedulable = false
	}
	return &Account{
		ID:          int64(len(s.created)),
		Name:        input.Name,
		Platform:    input.Platform,
		Type:        input.Type,
		Status:      status,
		Schedulable: schedulable,
	}, nil
}

func (s *contributionAdminStub) UpdateAccount(_ context.Context, id int64, input *UpdateAccountInput) (*Account, error) {
	s.updates = append(s.updates, adminUpdateCall{id: id, input: input})
	return &Account{ID: id}, nil
}

func (s *contributionAdminStub) SetAccountSchedulable(_ context.Context, id int64, schedulable bool) (*Account, error) {
	s.schedulables = append(s.schedulables, adminSchedulableCall{id: id, schedulable: schedulable})
	return &Account{ID: id}, nil
}

func (s *contributionAdminStub) DeleteAccount(_ context.Context, id int64) error {
	s.deleted = append(s.deleted, id)
	return nil
}

func (s *contributionAdminStub) ValidateAccountGroupBindings(context.Context, []int64) error {
	return nil
}

func newContributionServiceForTest(admin AdminService) (*AccountContributionService, *contributionRepoStub) {
	repo := newContributionRepoStub()
	return &AccountContributionService{
		repo:         repo,
		groupRepo:    newRewardGroupRepo(),
		adminService: admin,
	}, repo
}

func seedTask(t *testing.T, svc *AccountContributionService, mutate func(*AccountTaskCreateInput)) *AccountTask {
	t.Helper()
	in := &AccountTaskCreateInput{
		Name:               "贡献 OpenAI Key",
		Platform:           PlatformOpenAI,
		AccountTypes:       []string{AccountTypeAPIKey},
		RewardGroupID:      9,
		RewardDays:         30,
		MaxPerUser:         1,
		VerifyModels:       false,
		RejectOnVerifyFail: true,
		Enabled:            true,
	}
	if mutate != nil {
		mutate(in)
	}
	task, err := svc.CreateTask(context.Background(), in)
	require.NoError(t, err)
	return task
}

func TestSubmitCreatesInactiveAccountOutsideGroups(t *testing.T) {
	admin := &contributionAdminStub{}
	svc, repo := newContributionServiceForTest(admin)
	task := seedTask(t, svc, nil)

	sub, err := svc.Submit(context.Background(), &AccountSubmissionCreateInput{
		TaskID:      task.ID,
		UserID:      7,
		Name:        "我的 key",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
	})
	require.NoError(t, err)
	require.Equal(t, AccountSubmissionPending, sub.Status)

	// 账号必须以不绑分组、跳过默认分组、待审状态的方式创建，
	// 否则待审账号会立刻进入公共池或被调度器看到。
	require.Len(t, admin.created, 1)
	created := admin.created[0]
	require.Empty(t, created.GroupIDs)
	require.True(t, created.SkipDefaultGroupBind)
	require.True(t, created.PendingReview, "提交的账号必须从创建起就处于待审状态")

	// 创建阶段不应再额外写状态或调度开关。
	require.Empty(t, admin.schedulables)
	require.Empty(t, admin.updates)

	require.Len(t, repo.submissions, 1)
}

func TestSubmitRejectsDisabledTask(t *testing.T) {
	svc, _ := newContributionServiceForTest(&contributionAdminStub{})
	task := seedTask(t, svc, func(in *AccountTaskCreateInput) { in.Enabled = false })

	_, err := svc.Submit(context.Background(), &AccountSubmissionCreateInput{
		TaskID:      task.ID,
		UserID:      7,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
	})
	require.ErrorIs(t, err, ErrAccountTaskDisabled)
}

func TestSubmitRejectsPlatformMismatch(t *testing.T) {
	svc, _ := newContributionServiceForTest(&contributionAdminStub{})
	task := seedTask(t, svc, nil)

	_, err := svc.Submit(context.Background(), &AccountSubmissionCreateInput{
		TaskID:      task.ID,
		UserID:      7,
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
	})
	require.ErrorIs(t, err, ErrAccountTaskPlatform)
}

func TestSubmitRejectsDisallowedType(t *testing.T) {
	svc, _ := newContributionServiceForTest(&contributionAdminStub{})
	task := seedTask(t, svc, nil)

	_, err := svc.Submit(context.Background(), &AccountSubmissionCreateInput{
		TaskID:      task.ID,
		UserID:      7,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "tok"},
	})
	require.ErrorIs(t, err, ErrAccountTaskType)
}

func TestSubmitEnforcesMaxPerUser(t *testing.T) {
	svc, _ := newContributionServiceForTest(&contributionAdminStub{})
	task := seedTask(t, svc, nil)

	creds := map[string]any{"api_key": "sk-first"}
	_, err := svc.Submit(context.Background(), &AccountSubmissionCreateInput{
		TaskID: task.ID, UserID: 7, Platform: PlatformOpenAI,
		Type: AccountTypeAPIKey, Credentials: creds,
	})
	require.NoError(t, err)

	// 第二次提交超过上限，即使凭证不同也要拒绝。
	_, err = svc.Submit(context.Background(), &AccountSubmissionCreateInput{
		TaskID: task.ID, UserID: 7, Platform: PlatformOpenAI,
		Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-second"},
	})
	require.ErrorIs(t, err, ErrAccountTaskQuotaExceeded)
}

func TestSubmitRejectsDuplicateCredential(t *testing.T) {
	svc, _ := newContributionServiceForTest(&contributionAdminStub{})
	task := seedTask(t, svc, func(in *AccountTaskCreateInput) { in.MaxPerUser = 5 })

	creds := map[string]any{"api_key": "sk-same"}
	_, err := svc.Submit(context.Background(), &AccountSubmissionCreateInput{
		TaskID: task.ID, UserID: 7, Platform: PlatformOpenAI,
		Type: AccountTypeAPIKey, Credentials: creds,
	})
	require.NoError(t, err)

	// 同一份凭证换个用户提交也要拒绝，防止一人一份 key 反复刷奖励。
	_, err = svc.Submit(context.Background(), &AccountSubmissionCreateInput{
		TaskID: task.ID, UserID: 8, Platform: PlatformOpenAI,
		Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-same"},
	})
	require.ErrorIs(t, err, ErrAccountSubmissionExists)
}

func TestSubmitEnforcesExpectedBaseURL(t *testing.T) {
	svc, _ := newContributionServiceForTest(&contributionAdminStub{})
	task := seedTask(t, svc, func(in *AccountTaskCreateInput) {
		in.ExpectedBaseURL = "api.llm.ustc.edu.cn"
	})

	_, err := svc.Submit(context.Background(), &AccountSubmissionCreateInput{
		TaskID: task.ID, UserID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-1", "base_url": "https://evil.example.com"},
	})
	require.ErrorIs(t, err, ErrAccountTaskBaseURL)

	sub, err := svc.Submit(context.Background(), &AccountSubmissionCreateInput{
		TaskID: task.ID, UserID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-2", "base_url": "https://api.llm.ustc.edu.cn/v1"},
	})
	require.NoError(t, err)
	require.Equal(t, AccountSubmissionPending, sub.Status)
}

func TestApproveActivatesAccountAndBindsGroups(t *testing.T) {
	admin := &contributionAdminStub{}
	svc, _ := newContributionServiceForTest(admin)
	task := seedTask(t, svc, func(in *AccountTaskCreateInput) {
		in.TargetGroupIDs = []int64{4, 5}
	})

	sub, err := svc.Submit(context.Background(), &AccountSubmissionCreateInput{
		TaskID: task.ID, UserID: 7, Platform: PlatformOpenAI,
		Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-1"},
	})
	require.NoError(t, err)

	admin.updates = nil
	admin.schedulables = nil
	approved, err := svc.Approve(context.Background(), &ApproveInput{
		SubmissionID: sub.ID,
		ReviewerID:   1,
		GrantReward:  false,
	})
	require.NoError(t, err)
	require.Equal(t, AccountSubmissionApproved, approved.Status)

	require.Len(t, admin.schedulables, 1)
	require.True(t, admin.schedulables[0].schedulable)

	var groupUpdate *UpdateAccountInput
	for _, call := range admin.updates {
		if call.input.GroupIDs != nil {
			groupUpdate = call.input
		}
	}
	require.NotNil(t, groupUpdate)
	require.Equal(t, []int64{4, 5}, *groupUpdate.GroupIDs)
}

func TestApproveWithoutTargetGroupsLeavesAccountUngrouped(t *testing.T) {
	admin := &contributionAdminStub{}
	svc, _ := newContributionServiceForTest(admin)
	task := seedTask(t, svc, nil)

	sub, err := svc.Submit(context.Background(), &AccountSubmissionCreateInput{
		TaskID: task.ID, UserID: 7, Platform: PlatformOpenAI,
		Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-1"},
	})
	require.NoError(t, err)

	admin.updates = nil
	_, err = svc.Approve(context.Background(), &ApproveInput{
		SubmissionID: sub.ID, ReviewerID: 1, GrantReward: false,
	})
	require.NoError(t, err)

	for _, call := range admin.updates {
		require.Nil(t, call.input.GroupIDs, "未配置目标分组时不应写分组")
	}
}

func TestApproveRejectsNonPendingSubmission(t *testing.T) {
	admin := &contributionAdminStub{}
	svc, repo := newContributionServiceForTest(admin)
	task := seedTask(t, svc, nil)

	sub, err := svc.Submit(context.Background(), &AccountSubmissionCreateInput{
		TaskID: task.ID, UserID: 7, Platform: PlatformOpenAI,
		Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-1"},
	})
	require.NoError(t, err)
	stored := repo.submissions[sub.ID]
	stored.Status = AccountSubmissionApproved

	_, err = svc.Approve(context.Background(), &ApproveInput{SubmissionID: sub.ID, ReviewerID: 1})
	require.ErrorIs(t, err, ErrAccountSubmissionState)
}

func TestRejectClearsCredentialsAndDeletesAccount(t *testing.T) {
	admin := &contributionAdminStub{}
	svc, repo := newContributionServiceForTest(admin)
	task := seedTask(t, svc, nil)

	sub, err := svc.Submit(context.Background(), &AccountSubmissionCreateInput{
		TaskID: task.ID, UserID: 7, Platform: PlatformOpenAI,
		Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-1"},
	})
	require.NoError(t, err)

	rejected, err := svc.Reject(context.Background(), sub.ID, 1, "凭证无效")
	require.NoError(t, err)
	require.Equal(t, AccountSubmissionRejected, rejected.Status)
	require.Equal(t, "凭证无效", rejected.ReviewNote)
	require.Equal(t, []int64{sub.AccountID}, admin.deleted)

	// 拒绝后该用户可再次提交（不计入配额）。
	stored := repo.submissions[sub.ID]
	require.Equal(t, AccountSubmissionRejected, stored.Status)
}

func TestRejectRejectsNonPendingSubmission(t *testing.T) {
	svc, repo := newContributionServiceForTest(&contributionAdminStub{})
	task := seedTask(t, svc, nil)

	sub, err := svc.Submit(context.Background(), &AccountSubmissionCreateInput{
		TaskID: task.ID, UserID: 7, Platform: PlatformOpenAI,
		Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-1"},
	})
	require.NoError(t, err)
	repo.submissions[sub.ID].Status = AccountSubmissionRejected

	_, err = svc.Reject(context.Background(), sub.ID, 1, "")
	require.ErrorIs(t, err, ErrAccountSubmissionState)
}

func TestSubmitRecordsVerificationNotRunWhenDisabled(t *testing.T) {	svc, _ := newContributionServiceForTest(&contributionAdminStub{})
	task := seedTask(t, svc, func(in *AccountTaskCreateInput) { in.VerifyModels = false })

	sub, err := svc.Submit(context.Background(), &AccountSubmissionCreateInput{
		TaskID: task.ID, UserID: 7, Platform: PlatformOpenAI,
		Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-1"},
	})
	require.NoError(t, err)
	require.Equal(t, AccountVerifyNotRun, sub.VerificationStatus)
}

func TestSubmitRejectsCredentialWithoutSecret(t *testing.T) {
	svc, _ := newContributionServiceForTest(&contributionAdminStub{})
	task := seedTask(t, svc, func(in *AccountTaskCreateInput) { in.VerifyModels = true })

	_, err := svc.Submit(context.Background(), &AccountSubmissionCreateInput{
		TaskID: task.ID, UserID: 7, Platform: PlatformOpenAI,
		Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api.openai.com"},
	})
	require.ErrorIs(t, err, ErrAccountTaskVerifyFailed)
}

// 提交记录写入失败（例如并发撞上凭证指纹唯一索引）时必须回收已创建的账号，
// 否则会留下一条带用户凭证、却没有归属记录的孤儿账号。
func TestSubmitDiscardsAccountWhenSubmissionRecordFails(t *testing.T) {
	admin := &contributionAdminStub{}
	svc, repo := newContributionServiceForTest(admin)
	task := seedTask(t, svc, nil)
	repo.createSubmissionErr = errors.New("duplicate key value violates unique constraint")

	_, err := svc.Submit(context.Background(), &AccountSubmissionCreateInput{
		TaskID: task.ID, UserID: 7, Platform: PlatformOpenAI,
		Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-1"},
	})
	require.Error(t, err)

	require.Len(t, admin.created, 1, "账号仍然会被创建")
	require.Equal(t, []int64{1}, admin.deleted, "创建出的账号必须被回收")
	require.Empty(t, repo.submissions)
}

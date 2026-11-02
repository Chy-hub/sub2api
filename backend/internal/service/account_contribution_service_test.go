package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---- 测试替身 ----

type contributionRepoStub struct {
	tasks       map[int64]*AccountTask
	submissions map[int64]*AccountSubmission
	nextTaskID  int64
	nextSubID   int64

	createTaskErr       error
	createSubmissionErr error

	userSubmissions map[[2]int64]int
	rewarded        map[[2]int64]bool
}

func newContributionRepoStub() *contributionRepoStub {
	return &contributionRepoStub{
		tasks:           map[int64]*AccountTask{},
		submissions:     map[int64]*AccountSubmission{},
		userSubmissions: map[[2]int64]int{},
		rewarded:        map[[2]int64]bool{},
	}
}

func (s *contributionRepoStub) CreateTask(_ context.Context, task *AccountTask) error {
	if s.createTaskErr != nil {
		return s.createTaskErr
	}
	s.nextTaskID++
	task.ID = s.nextTaskID
	task.CreatedAt = time.Now().UTC()
	task.UpdatedAt = task.CreatedAt
	cloned := *task
	s.tasks[task.ID] = &cloned
	return nil
}

func (s *contributionRepoStub) UpdateTask(_ context.Context, task *AccountTask) error {
	cloned := *task
	s.tasks[task.ID] = &cloned
	return nil
}

func (s *contributionRepoStub) GetTaskByID(_ context.Context, id int64) (*AccountTask, error) {
	task, ok := s.tasks[id]
	if !ok {
		return nil, nil
	}
	cloned := *task
	return &cloned, nil
}

func (s *contributionRepoStub) ListTasks(_ context.Context, enabledOnly bool) ([]*AccountTask, error) {
	result := make([]*AccountTask, 0, len(s.tasks))
	for _, task := range s.tasks {
		if enabledOnly && !task.Enabled {
			continue
		}
		cloned := *task
		result = append(result, &cloned)
	}
	return result, nil
}

func (s *contributionRepoStub) DeleteTask(_ context.Context, id int64) error {
	delete(s.tasks, id)
	return nil
}

func (s *contributionRepoStub) CreateSubmission(_ context.Context, sub *AccountSubmission) error {
	if s.createSubmissionErr != nil {
		return s.createSubmissionErr
	}
	s.nextSubID++
	sub.ID = s.nextSubID
	sub.CreatedAt = time.Now().UTC()
	sub.UpdatedAt = sub.CreatedAt
	cloned := *sub
	s.submissions[sub.ID] = &cloned
	key := [2]int64{sub.TaskID, sub.UserID}
	if sub.Status != AccountSubmissionRejected {
		s.userSubmissions[key]++
	}
	return nil
}

func (s *contributionRepoStub) UpdateSubmission(_ context.Context, sub *AccountSubmission) error {
	cloned := *sub
	s.submissions[sub.ID] = &cloned
	return nil
}

func (s *contributionRepoStub) GetSubmissionByID(_ context.Context, id int64) (*AccountSubmission, error) {
	sub, ok := s.submissions[id]
	if !ok {
		return nil, nil
	}
	cloned := *sub
	return &cloned, nil
}

func (s *contributionRepoStub) ListSubmissionsByUser(_ context.Context, userID int64) ([]*AccountSubmission, error) {
	result := make([]*AccountSubmission, 0)
	for _, sub := range s.submissions {
		if sub.UserID != userID {
			continue
		}
		cloned := *sub
		result = append(result, &cloned)
	}
	return result, nil
}

func (s *contributionRepoStub) ListPendingSubmissions(_ context.Context, taskID int64) ([]*AccountSubmission, error) {
	result := make([]*AccountSubmission, 0)
	for _, sub := range s.submissions {
		if sub.Status != AccountSubmissionPending {
			continue
		}
		if taskID > 0 && sub.TaskID != taskID {
			continue
		}
		cloned := *sub
		result = append(result, &cloned)
	}
	return result, nil
}

func (s *contributionRepoStub) CountUserSubmissions(_ context.Context, taskID, userID int64) (int, error) {
	return s.userSubmissions[[2]int64{taskID, userID}], nil
}

func (s *contributionRepoStub) FindActiveByFingerprint(_ context.Context, fingerprint string) (*AccountSubmission, error) {
	if fingerprint == "" {
		return nil, nil
	}
	for _, sub := range s.submissions {
		if sub.CredentialFingerprint == fingerprint && sub.Status != AccountSubmissionRejected {
			cloned := *sub
			return &cloned, nil
		}
	}
	return nil, nil
}

func (s *contributionRepoStub) HasUserReward(_ context.Context, taskID, userID int64) (bool, error) {
	return s.rewarded[[2]int64{taskID, userID}], nil
}

// ---- 纯函数测试 ----

func TestCredentialFingerprintIgnoresTimestamps(t *testing.T) {
	base := map[string]any{
		"api_key":    "sk-abc",
		"base_url":   "https://api.example.com",
		"expires_at": "2026-01-01T00:00:00Z",
	}
	other := map[string]any{
		"base_url":   "https://api.example.com",
		"api_key":    "sk-abc",
		"expires_at": "2027-09-09T00:00:00Z",
	}
	require.Equal(t,
		credentialFingerprint("openai", "apikey", base),
		credentialFingerprint("openai", "apikey", other),
	)
}

func TestCredentialFingerprintDiffersByCredential(t *testing.T) {
	a := map[string]any{"api_key": "sk-aaa"}
	b := map[string]any{"api_key": "sk-bbb"}
	require.NotEqual(t,
		credentialFingerprint("openai", "apikey", a),
		credentialFingerprint("openai", "apikey", b),
	)
}

func TestCredentialFingerprintDiffersByPlatform(t *testing.T) {
	creds := map[string]any{"api_key": "sk-abc"}
	require.NotEqual(t,
		credentialFingerprint("openai", "apikey", creds),
		credentialFingerprint("anthropic", "apikey", creds),
	)
}

func TestNormalizeBaseURLHost(t *testing.T) {
	cases := map[string]string{
		"https://API.LLM.USTC.EDU.CN/v1": "api.llm.ustc.edu.cn",
		"api.llm.ustc.edu.cn":            "api.llm.ustc.edu.cn",
		"  https://Example.com/path  ":   "example.com",
		"":                               "",
	}
	for input, want := range cases {
		require.Equal(t, want, normalizeBaseURLHost(input), "input=%q", input)
	}
}

func TestCheckExpectedBaseURL(t *testing.T) {
	svc := &AccountContributionService{}

	task := &AccountTask{ExpectedBaseURL: "api.llm.ustc.edu.cn"}
	require.NoError(t, svc.checkExpectedBaseURL(task, map[string]any{
		"base_url": "https://api.llm.ustc.edu.cn/v1",
	}))

	// 主机不匹配必须拒绝，这是防止凭证被提交到其他上游的关键约束。
	require.ErrorIs(t, svc.checkExpectedBaseURL(task, map[string]any{
		"base_url": "https://evil.example.com",
	}), ErrAccountTaskBaseURL)

	// 任务限定上游时，缺失 base_url 也视为不匹配，避免绕过约束。
	require.ErrorIs(t, svc.checkExpectedBaseURL(task, map[string]any{}), ErrAccountTaskBaseURL)

	// 任务未限定上游时不做限制。
	open := &AccountTask{}
	require.NoError(t, svc.checkExpectedBaseURL(open, map[string]any{}))
	require.NoError(t, svc.checkExpectedBaseURL(open, map[string]any{"base_url": "https://any.example.com"}))
}

// api_base_urls 是多协议账号真正生效的地址字段，只查 base_url 会留下绕过路径。
func TestCheckExpectedBaseURLRejectsMismatchedProtocolBaseURLs(t *testing.T) {
	svc := &AccountContributionService{}
	task := &AccountTask{ExpectedBaseURL: "api.llm.ustc.edu.cn"}

	require.ErrorIs(t, svc.checkExpectedBaseURL(task, map[string]any{
		"base_url": "https://api.llm.ustc.edu.cn",
		"api_base_urls": map[string]any{
			"chat_completions": "https://api.llm.ustc.edu.cn",
			"anthropic":        "https://evil.example.com",
		},
	}), ErrAccountTaskBaseURL, "任一协议地址偏离限定上游都必须拒绝")

	// 全部协议地址一致时放行。
	require.NoError(t, svc.checkExpectedBaseURL(task, map[string]any{
		"base_url": "https://api.llm.ustc.edu.cn",
		"api_base_urls": map[string]any{
			"chat_completions": "https://api.llm.ustc.edu.cn/v1",
			"responses":        "https://api.llm.ustc.edu.cn/v1",
		},
	}))

	// 只有 api_base_urls、没有 base_url 也不能绕过。
	require.NoError(t, svc.checkExpectedBaseURL(task, map[string]any{
		"api_base_urls": map[string]any{
			"chat_completions": "https://api.llm.ustc.edu.cn",
		},
	}))
	require.ErrorIs(t, svc.checkExpectedBaseURL(task, map[string]any{
		"api_base_urls": map[string]any{
			"chat_completions": "https://evil.example.com",
		},
	}), ErrAccountTaskBaseURL)
}

func TestCollectCredentialBaseURLHosts(t *testing.T) {
	require.Empty(t, collectCredentialBaseURLHosts(map[string]any{}))

	hosts := collectCredentialBaseURLHosts(map[string]any{
		"base_url": "https://API.Example.com/v1",
		"api_base_urls": map[string]any{
			"anthropic": "https://b.example.com",
			"chat":      "https://c.example.com",
		},
	})
	require.Equal(t, []string{"api.example.com", "b.example.com", "c.example.com"}, hosts)

	// 非字符串值忽略，不 panic。
	require.Empty(t, collectCredentialBaseURLHosts(map[string]any{
		"base_url":      123,
		"api_base_urls": "not-a-map",
	}))
}

func TestNormalizeStringListDeduplicates(t *testing.T) {
	got := normalizeStringList([]string{"apikey", " apikey ", "oauth", "", "apikey"})
	require.Equal(t, []string{"apikey", "oauth"}, got)
}

func TestNormalizeInt64ListDropsInvalid(t *testing.T) {
	got := normalizeInt64List([]int64{3, 0, -1, 3, 7})
	require.Equal(t, []int64{3, 7}, got)
}

func TestValidateAccountTaskInputDefaults(t *testing.T) {
	in := &AccountTaskCreateInput{
		Name:          "贡献 USTC 额度",
		Platform:      "openai",
		AccountTypes:  []string{"apikey"},
		RewardGroupID: 5,
		RewardDays:    30,
	}
	require.NoError(t, validateAccountTaskInput(in))
	require.Equal(t, 1, in.MaxPerUser)
	require.Equal(t, 3, in.Concurrency)
	require.Equal(t, 50, in.Priority)
}

func TestValidateAccountTaskInputRejectsMissingFields(t *testing.T) {
	cases := []*AccountTaskCreateInput{
		{Platform: "openai", AccountTypes: []string{"apikey"}, RewardGroupID: 1, RewardDays: 30},
		{Name: "n", AccountTypes: []string{"apikey"}, RewardGroupID: 1, RewardDays: 30},
		{Name: "n", Platform: "openai", RewardGroupID: 1, RewardDays: 30},
		{Name: "n", Platform: "openai", AccountTypes: []string{"apikey"}, RewardDays: 30},
		{Name: "n", Platform: "openai", AccountTypes: []string{"apikey"}, RewardGroupID: 1},
	}
	for i, in := range cases {
		require.Error(t, validateAccountTaskInput(in), "case %d should fail", i)
	}
}

// ---- 服务编排测试 ----

type contributionGroupRepoStub struct {
	GroupRepository
	groups map[int64]*Group
}

func (s *contributionGroupRepoStub) GetByID(_ context.Context, id int64) (*Group, error) {
	group, ok := s.groups[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cloned := *group
	return &cloned, nil
}

func newRewardGroupRepo() *contributionGroupRepoStub {
	return &contributionGroupRepoStub{groups: map[int64]*Group{
		9: {
			ID:               9,
			Status:           StatusActive,
			SubscriptionType: SubscriptionTypeSubscription,
		},
		10: {
			ID:               10,
			Status:           StatusActive,
			SubscriptionType: SubscriptionTypeStandard,
		},
	}}
}

func TestCreateTaskRequiresSubscriptionRewardGroup(t *testing.T) {
	repo := newContributionRepoStub()
	svc := &AccountContributionService{repo: repo, groupRepo: newRewardGroupRepo()}

	_, err := svc.CreateTask(context.Background(), &AccountTaskCreateInput{
		Name:          "任务",
		Platform:      "openai",
		AccountTypes:  []string{"apikey"},
		RewardGroupID: 10, // 标准分组，不是订阅型
		RewardDays:    30,
	})
	require.ErrorIs(t, err, ErrAccountTaskRewardGroup)

	// 订阅型分组可以通过。
	task, err := svc.CreateTask(context.Background(), &AccountTaskCreateInput{
		Name:          "任务",
		Platform:      "openai",
		AccountTypes:  []string{"apikey"},
		RewardGroupID: 9,
		RewardDays:    30,
	})
	require.NoError(t, err)
	require.NotZero(t, task.ID)
}

func TestUpdateTaskRejectsInvalidChanges(t *testing.T) {
	repo := newContributionRepoStub()
	svc := &AccountContributionService{repo: repo, groupRepo: newRewardGroupRepo()}
	task, err := svc.CreateTask(context.Background(), &AccountTaskCreateInput{
		Name:          "任务",
		Platform:      "openai",
		AccountTypes:  []string{"apikey"},
		RewardGroupID: 9,
		RewardDays:    30,
	})
	require.NoError(t, err)

	zero := 0
	_, err = svc.UpdateTask(context.Background(), task.ID, &AccountTaskUpdateInput{MaxPerUser: &zero})
	require.Error(t, err)

	badGroup := int64(10)
	_, err = svc.UpdateTask(context.Background(), task.ID, &AccountTaskUpdateInput{RewardGroupID: &badGroup})
	require.ErrorIs(t, err, ErrAccountTaskRewardGroup)

	enabled := false
	updated, err := svc.UpdateTask(context.Background(), task.ID, &AccountTaskUpdateInput{Enabled: &enabled})
	require.NoError(t, err)
	require.False(t, updated.Enabled)
}

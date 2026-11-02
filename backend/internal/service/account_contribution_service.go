package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
)

// 账号贡献任务相关错误。
var (
	ErrAccountTaskNotFound       = infraerrors.NotFound("ACCOUNT_TASK_NOT_FOUND", "account task not found")
	ErrAccountTaskDisabled       = infraerrors.BadRequest("ACCOUNT_TASK_DISABLED", "account task is disabled")
	ErrAccountTaskPlatform       = infraerrors.BadRequest("ACCOUNT_TASK_PLATFORM_MISMATCH", "account platform does not match the task")
	ErrAccountTaskType           = infraerrors.BadRequest("ACCOUNT_TASK_TYPE_MISMATCH", "account type is not allowed by the task")
	ErrAccountTaskBaseURL        = infraerrors.BadRequest("ACCOUNT_TASK_BASE_URL_MISMATCH", "base_url does not match the task requirement")
	ErrAccountTaskQuotaExceeded  = infraerrors.BadRequest("ACCOUNT_TASK_QUOTA_EXCEEDED", "submission limit reached for this task")
	ErrAccountSubmissionExists   = infraerrors.Conflict("ACCOUNT_SUBMISSION_EXISTS", "this credential has already been submitted")
	ErrAccountSubmissionNotFound = infraerrors.NotFound("ACCOUNT_SUBMISSION_NOT_FOUND", "account submission not found")
	ErrAccountSubmissionState    = infraerrors.Conflict("ACCOUNT_SUBMISSION_STATE_INVALID", "account submission is not in a reviewable state")
	ErrAccountTaskRewardGroup    = infraerrors.BadRequest("ACCOUNT_TASK_REWARD_GROUP_INVALID", "reward group must be an active subscription group")
	ErrAccountTaskVerifyFailed   = infraerrors.BadRequest("ACCOUNT_TASK_VERIFY_FAILED", "credential verification failed")
)

// AccountDeliveryStatus 表示提交记录状态。
const (
	AccountSubmissionPending  = "pending"
	AccountSubmissionApproved = "approved"
	AccountSubmissionRejected = "rejected"
)

// 校验状态。
const (
	AccountVerifyNotRun      = "not_run"
	AccountVerifyPassed      = "passed"
	AccountVerifyFailed      = "failed"
	AccountVerifyUnsupported = "unsupported"
)

// AccountTask 是管理员发布的账号贡献任务。
type AccountTask struct {
	ID                  int64
	Name                string
	Description         string
	Enabled             bool
	Platform            string
	AccountTypes        []string
	TargetGroupIDs      []int64
	RewardGroupID       int64
	RewardDays          int
	MaxPerUser          int
	ExpectedBaseURL     string
	VerifyModels        bool
	RejectOnVerifyFail  bool
	Priority            int
	Concurrency         int
	RateMultiplier      *float64
	SortOrder           int
	CreatedAt           time.Time
	UpdatedAt           time.Time
	DeletedAt           *time.Time
}

// AccountSubmission 是一条用户提交记录。
type AccountSubmission struct {
	ID                    int64
	TaskID                int64
	UserID                int64
	AccountID             int64
	Status                string
	VerificationStatus    string
	VerificationError     string
	ModelCount            int
	CredentialFingerprint string
	ReviewedBy            *int64
	ReviewedAt            *time.Time
	ReviewNote            string
	RewardSubscriptionID  *int64
	RewardGrantedAt       *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// AccountContributionRepository 持久化任务与提交记录。
type AccountContributionRepository interface {
	CreateTask(ctx context.Context, task *AccountTask) error
	UpdateTask(ctx context.Context, task *AccountTask) error
	GetTaskByID(ctx context.Context, id int64) (*AccountTask, error)
	ListTasks(ctx context.Context, enabledOnly bool) ([]*AccountTask, error)
	DeleteTask(ctx context.Context, id int64) error

	CreateSubmission(ctx context.Context, sub *AccountSubmission) error
	UpdateSubmission(ctx context.Context, sub *AccountSubmission) error
	GetSubmissionByID(ctx context.Context, id int64) (*AccountSubmission, error)
	ListSubmissionsByUser(ctx context.Context, userID int64) ([]*AccountSubmission, error)
	ListPendingSubmissions(ctx context.Context, taskID int64) ([]*AccountSubmission, error)
	CountUserSubmissions(ctx context.Context, taskID, userID int64) (int, error)
	FindActiveByFingerprint(ctx context.Context, fingerprint string) (*AccountSubmission, error)
	HasUserReward(ctx context.Context, taskID, userID int64) (bool, error)
}

// AccountTaskCreateInput 创建任务输入。
type AccountTaskCreateInput struct {
	Name               string
	Description        string
	Platform           string
	AccountTypes       []string
	TargetGroupIDs     []int64
	RewardGroupID      int64
	RewardDays         int
	MaxPerUser         int
	ExpectedBaseURL    string
	VerifyModels       bool
	RejectOnVerifyFail bool
	Priority           int
	Concurrency        int
	RateMultiplier     *float64
	SortOrder          int
	Enabled            bool
}

// AccountTaskUpdateInput 更新任务输入；nil 表示不修改。
type AccountTaskUpdateInput struct {
	Name               *string
	Description        *string
	Platform           *string
	AccountTypes       *[]string
	TargetGroupIDs     *[]int64
	RewardGroupID      *int64
	RewardDays         *int
	MaxPerUser         *int
	ExpectedBaseURL    *string
	VerifyModels       *bool
	RejectOnVerifyFail *bool
	Priority           *int
	Concurrency        *int
	RateMultiplier     *float64
	ClearRateMultiplier bool
	SortOrder          *int
	Enabled            *bool
}

// AccountSubmissionCreateInput 用户提交输入。
type AccountSubmissionCreateInput struct {
	TaskID      int64
	UserID      int64
	Name        string
	Platform    string
	Type        string
	Credentials map[string]any
}

// AccountContributionService 负责任务的增删改查、提交校验与审核发放。
type AccountContributionService struct {
	repo            AccountContributionRepository
	groupRepo       GroupRepository
	accountRepo     AccountRepository
	adminService    AdminService
	subscriptionSvc *SubscriptionService
	testService     *AccountTestService
	cfg             *config.Config
}

// NewAccountContributionService 构造服务。
func NewAccountContributionService(
	repo AccountContributionRepository,
	groupRepo GroupRepository,
	accountRepo AccountRepository,
	adminService AdminService,
	subscriptionSvc *SubscriptionService,
	testService *AccountTestService,
	cfg *config.Config,
) *AccountContributionService {
	return &AccountContributionService{
		repo:            repo,
		groupRepo:       groupRepo,
		accountRepo:     accountRepo,
		adminService:    adminService,
		subscriptionSvc: subscriptionSvc,
		testService:     testService,
		cfg:             cfg,
	}
}

// ---------- 任务管理 ----------

// AllowedAccountTypesForPlatform 返回该平台允许的账号类型。
// 与 buildAccountForCreate 的硬约束保持一致，供任务配置在下发前就拦截
// 必然创建失败的组合（例如 Qoder CN 只能收 reverse 账号）。
func AllowedAccountTypesForPlatform(platform string) []string {
	trimmed := strings.TrimSpace(platform)
	if trimmed == "" {
		return nil
	}
	// 未知平台返回 nil，让调用方按「不支持」处理；
	// 否则拼错平台名会落到普通分支被静默放行。
	if !domain.IsConcretePlatform(trimmed) {
		return nil
	}
	if IsReversePlatform(trimmed) {
		return []string{AccountTypeReverse}
	}
	if trimmed == PlatformTypeSafe {
		return []string{AccountTypeAPIKey}
	}
	types := []string{AccountTypeAPIKey, AccountTypeUpstream, AccountTypeOAuth, AccountTypeSetupToken}
	switch trimmed {
	case PlatformGemini:
		types = append(types, AccountTypeServiceAccount)
	case PlatformAnthropic:
		types = append(types, AccountTypeBedrock)
	}
	return types
}

// validatePlatformAccountTypes 校验任务声明的账号类型都能在该平台成立。
func validatePlatformAccountTypes(platform string, types []string) error {
	allowed := AllowedAccountTypesForPlatform(platform)
	if len(allowed) == 0 {
		return infraerrors.BadRequest("ACCOUNT_TASK_PLATFORM_INVALID", "unknown platform")
	}
	for _, accountType := range types {
		if !slices.Contains(allowed, accountType) {
			return infraerrors.BadRequest("ACCOUNT_TASK_TYPE_UNSUPPORTED",
				fmt.Sprintf("account type %q is not supported on platform %q", accountType, platform))
		}
	}
	return nil
}

const accountTaskMaxNameRunes = 100

// accountContributionProbeTimeout 限制提交时的凭证探测耗时。
// 这是同步请求，超时过长会拖住用户提交接口。
const accountContributionProbeTimeout = 15 * time.Second

func validateAccountTaskInput(in *AccountTaskCreateInput) error {
	if strings.TrimSpace(in.Name) == "" {
		return infraerrors.BadRequest("ACCOUNT_TASK_NAME_REQUIRED", "task name is required")
	}
	if len([]rune(in.Name)) > accountTaskMaxNameRunes {
		return infraerrors.BadRequest("ACCOUNT_TASK_NAME_TOO_LONG", "task name is too long")
	}
	if strings.TrimSpace(in.Platform) == "" {
		return infraerrors.BadRequest("ACCOUNT_TASK_PLATFORM_REQUIRED", "task platform is required")
	}
	if len(in.AccountTypes) == 0 {
		return infraerrors.BadRequest("ACCOUNT_TASK_TYPES_REQUIRED", "at least one account type is required")
	}
	if in.RewardGroupID <= 0 {
		return infraerrors.BadRequest("ACCOUNT_TASK_REWARD_GROUP_REQUIRED", "reward group is required")
	}
	if in.RewardDays <= 0 {
		return infraerrors.BadRequest("ACCOUNT_TASK_REWARD_DAYS_INVALID", "reward days must be positive")
	}
	if in.MaxPerUser <= 0 {
		in.MaxPerUser = 1
	}
	if in.Concurrency <= 0 {
		in.Concurrency = 3
	}
	if in.Priority <= 0 {
		in.Priority = 50
	}
	if err := validatePlatformAccountTypes(in.Platform, in.AccountTypes); err != nil {
		return err
	}
	return nil
}

// validateRewardGroup 奖励分组必须是活跃的订阅型分组，否则发放订阅会直接失败。
func (s *AccountContributionService) validateRewardGroup(ctx context.Context, groupID int64) error {
	group, err := s.groupRepo.GetByID(ctx, groupID)
	if err != nil {
		return ErrAccountTaskRewardGroup
	}
	if group == nil || !group.IsActive() || !group.IsSubscriptionType() {
		return ErrAccountTaskRewardGroup
	}
	return nil
}

// CreateTask 创建任务。
func (s *AccountContributionService) CreateTask(ctx context.Context, in *AccountTaskCreateInput) (*AccountTask, error) {
	if err := validateAccountTaskInput(in); err != nil {
		return nil, err
	}
	if err := s.validateRewardGroup(ctx, in.RewardGroupID); err != nil {
		return nil, err
	}
	if len(in.TargetGroupIDs) > 0 {
		if err := s.adminService.ValidateAccountGroupBindings(ctx, in.TargetGroupIDs); err != nil {
			return nil, err
		}
	}
	task := &AccountTask{
		Name:               strings.TrimSpace(in.Name),
		Description:        in.Description,
		Enabled:            in.Enabled,
		Platform:           strings.TrimSpace(in.Platform),
		AccountTypes:       normalizeStringList(in.AccountTypes),
		TargetGroupIDs:     normalizeInt64List(in.TargetGroupIDs),
		RewardGroupID:      in.RewardGroupID,
		RewardDays:         in.RewardDays,
		MaxPerUser:         in.MaxPerUser,
		ExpectedBaseURL:    normalizeBaseURLHost(in.ExpectedBaseURL),
		VerifyModels:       in.VerifyModels,
		RejectOnVerifyFail: in.RejectOnVerifyFail,
		Priority:           in.Priority,
		Concurrency:        in.Concurrency,
		RateMultiplier:     in.RateMultiplier,
		SortOrder:          in.SortOrder,
	}
	if err := s.repo.CreateTask(ctx, task); err != nil {
		return nil, err
	}
	return task, nil
}

// UpdateTask 更新任务。
func (s *AccountContributionService) UpdateTask(ctx context.Context, id int64, in *AccountTaskUpdateInput) (*AccountTask, error) {
	task, err := s.repo.GetTaskByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, ErrAccountTaskNotFound
	}
	if in.Name != nil {
		if strings.TrimSpace(*in.Name) == "" {
			return nil, infraerrors.BadRequest("ACCOUNT_TASK_NAME_REQUIRED", "task name is required")
		}
		task.Name = strings.TrimSpace(*in.Name)
	}
	if in.Description != nil {
		task.Description = *in.Description
	}
	if in.Platform != nil {
		if strings.TrimSpace(*in.Platform) == "" {
			return nil, infraerrors.BadRequest("ACCOUNT_TASK_PLATFORM_REQUIRED", "task platform is required")
		}
		task.Platform = strings.TrimSpace(*in.Platform)
	}
	if in.AccountTypes != nil {
		if len(*in.AccountTypes) == 0 {
			return nil, infraerrors.BadRequest("ACCOUNT_TASK_TYPES_REQUIRED", "at least one account type is required")
		}
		task.AccountTypes = normalizeStringList(*in.AccountTypes)
	}
	if in.TargetGroupIDs != nil {
		if len(*in.TargetGroupIDs) > 0 {
			if err := s.adminService.ValidateAccountGroupBindings(ctx, *in.TargetGroupIDs); err != nil {
				return nil, err
			}
		}
		task.TargetGroupIDs = normalizeInt64List(*in.TargetGroupIDs)
	}
	if in.RewardGroupID != nil {
		if err := s.validateRewardGroup(ctx, *in.RewardGroupID); err != nil {
			return nil, err
		}
		task.RewardGroupID = *in.RewardGroupID
	}
	if in.RewardDays != nil {
		if *in.RewardDays <= 0 {
			return nil, infraerrors.BadRequest("ACCOUNT_TASK_REWARD_DAYS_INVALID", "reward days must be positive")
		}
		task.RewardDays = *in.RewardDays
	}
	if in.MaxPerUser != nil {
		if *in.MaxPerUser <= 0 {
			return nil, infraerrors.BadRequest("ACCOUNT_TASK_MAX_PER_USER_INVALID", "max submissions per user must be positive")
		}
		task.MaxPerUser = *in.MaxPerUser
	}
	if in.ExpectedBaseURL != nil {
		task.ExpectedBaseURL = normalizeBaseURLHost(*in.ExpectedBaseURL)
	}
	if in.VerifyModels != nil {
		task.VerifyModels = *in.VerifyModels
	}
	if in.RejectOnVerifyFail != nil {
		task.RejectOnVerifyFail = *in.RejectOnVerifyFail
	}
	if in.Priority != nil {
		if *in.Priority <= 0 {
			return nil, infraerrors.BadRequest("ACCOUNT_TASK_PRIORITY_INVALID", "priority must be positive")
		}
		task.Priority = *in.Priority
	}
	if in.Concurrency != nil {
		if *in.Concurrency <= 0 {
			return nil, infraerrors.BadRequest("ACCOUNT_TASK_CONCURRENCY_INVALID", "concurrency must be positive")
		}
		task.Concurrency = *in.Concurrency
	}
	if in.ClearRateMultiplier {
		task.RateMultiplier = nil
	} else if in.RateMultiplier != nil {
		if *in.RateMultiplier < 0 {
			return nil, infraerrors.BadRequest("ACCOUNT_TASK_RATE_MULTIPLIER_INVALID", "rate multiplier must be >= 0")
		}
		task.RateMultiplier = in.RateMultiplier
	}
	if in.SortOrder != nil {
		task.SortOrder = *in.SortOrder
	}
	if in.Enabled != nil {
		task.Enabled = *in.Enabled
	}
	// 平台和账号类型可以分开更新，所以必须按最终组合再校验一次：
	// 只改平台不改类型时，原类型可能在新平台上不成立。
	if err := validatePlatformAccountTypes(task.Platform, task.AccountTypes); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateTask(ctx, task); err != nil {
		return nil, err
	}
	return task, nil
}

// ListTasks 列出任务。
func (s *AccountContributionService) ListTasks(ctx context.Context, enabledOnly bool) ([]*AccountTask, error) {
	return s.repo.ListTasks(ctx, enabledOnly)
}

// GetTask 读取单个任务。
func (s *AccountContributionService) GetTask(ctx context.Context, id int64) (*AccountTask, error) {
	task, err := s.repo.GetTaskByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, ErrAccountTaskNotFound
	}
	return task, nil
}

// DeleteTask 软删除任务。
func (s *AccountContributionService) DeleteTask(ctx context.Context, id int64) error {
	if _, err := s.GetTask(ctx, id); err != nil {
		return err
	}
	return s.repo.DeleteTask(ctx, id)
}

// ---------- 用户提交 ----------

// Submit 校验并落库一条提交。账号以 inactive 创建，审核通过后才激活。
func (s *AccountContributionService) Submit(ctx context.Context, in *AccountSubmissionCreateInput) (*AccountSubmission, error) {
	task, err := s.repo.GetTaskByID(ctx, in.TaskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, ErrAccountTaskNotFound
	}
	if !task.Enabled {
		return nil, ErrAccountTaskDisabled
	}
	if strings.TrimSpace(in.Platform) != task.Platform {
		return nil, ErrAccountTaskPlatform
	}
	if !slices.Contains(task.AccountTypes, in.Type) {
		return nil, ErrAccountTaskType
	}
	// 纵深防御：任务配置可能来自历史数据，提交时按平台规则再确认一次，
	// 避免把平台根本不支持的账号类型建出来。
	if err := validatePlatformAccountTypes(in.Platform, []string{in.Type}); err != nil {
		return nil, ErrAccountTaskType
	}
	// 任务锁定了上游时，用户不需要（也不应该）自己填 base_url：
	// 由服务端补全，用户只提供凭证本身。这既省掉一个必然填错的输入，
	// 也保证地址一定落在任务限定的主机上。
	in.Credentials = s.applyExpectedBaseURL(task, in.Credentials)
	if err := s.checkExpectedBaseURL(task, in.Credentials); err != nil {
		return nil, err
	}

	count, err := s.repo.CountUserSubmissions(ctx, task.ID, in.UserID)
	if err != nil {
		return nil, err
	}
	if count >= task.MaxPerUser {
		return nil, ErrAccountTaskQuotaExceeded
	}

	fingerprint := credentialFingerprint(in.Platform, in.Type, in.Credentials)
	if fingerprint != "" {
		existing, err := s.repo.FindActiveByFingerprint(ctx, fingerprint)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return nil, ErrAccountSubmissionExists
		}
	}

	verifyStatus, verifyErr, modelCount := s.verifyCredential(ctx, task, in)
	if verifyStatus == AccountVerifyFailed && task.RejectOnVerifyFail {
		return nil, fmt.Errorf("%w: %s", ErrAccountTaskVerifyFailed, verifyErr)
	}

	createInput := &CreateAccountInput{
		Name:                  buildSubmittedAccountName(task, in),
		Platform:              in.Platform,
		Type:                  in.Type,
		Credentials:           in.Credentials,
		Extra:                 map[string]any{},
		Concurrency:           task.Concurrency,
		Priority:              task.Priority,
		GroupIDs:              nil,
		SkipDefaultGroupBind:  true,
		SkipMixedChannelCheck: true,
		// 待审账号必须从创建起就不可调度：先建活跃账号再冻结之间存在窗口。
		PendingReview: true,
	}
	account, err := s.adminService.CreateAccount(ctx, createInput)
	if err != nil {
		return nil, err
	}

	sub := &AccountSubmission{
		TaskID:                task.ID,
		UserID:                in.UserID,
		AccountID:             account.ID,
		Status:                AccountSubmissionPending,
		VerificationStatus:    verifyStatus,
		VerificationError:     verifyErr,
		ModelCount:            modelCount,
		CredentialFingerprint: fingerprint,
	}
	if err := s.repo.CreateSubmission(ctx, sub); err != nil {
		// 提交记录没写成功，账号就成了没有任何归属的孤儿（且带着用户凭证）。
		// 并发提交撞上指纹唯一索引时就会走到这里，必须回收。
		if cleanupErr := s.discardSubmittedAccount(ctx, account.ID); cleanupErr != nil {
			logger.LegacyPrintf("service.account_contribution",
				"discard orphan submission account failed: account=%d err=%v", account.ID, cleanupErr)
		}
		return nil, err
	}
	return sub, nil
}

// discardSubmittedAccount 回收未成功建立提交记录的账号：先清空凭证再软删。
func (s *AccountContributionService) discardSubmittedAccount(ctx context.Context, accountID int64) error {
	if updater, ok := any(s.accountRepo).(accountCredentialsUpdater); ok {
		if err := updater.UpdateCredentials(ctx, accountID, map[string]any{}); err != nil {
			logger.LegacyPrintf("service.account_contribution",
				"clear orphan credentials failed: account=%d err=%v", accountID, err)
		}
	}
	return s.adminService.DeleteAccount(ctx, accountID)
}

func buildSubmittedAccountName(task *AccountTask, in *AccountSubmissionCreateInput) string {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = fmt.Sprintf("%s - submission", task.Name)
	}
	if runes := []rune(name); len(runes) > accountTaskMaxNameRunes {
		name = string(runes[:accountTaskMaxNameRunes])
	}
	return name
}

// applyExpectedBaseURL 在任务锁定上游、且用户未提供 base_url 时补全地址。
// 返回新的凭证 map，不修改调用方的入参。
func (s *AccountContributionService) applyExpectedBaseURL(task *AccountTask, credentials map[string]any) map[string]any {
	if task.ExpectedBaseURL == "" {
		return credentials
	}
	if raw, ok := credentials["base_url"].(string); ok && strings.TrimSpace(raw) != "" {
		return credentials
	}
	out := make(map[string]any, len(credentials)+1)
	for key, value := range credentials {
		out[key] = value
	}
	out["base_url"] = "https://" + task.ExpectedBaseURL
	return out
}

// checkExpectedBaseURL 在任务限定上游主机时校验提交的 base_url。
//
// 注意 base_url 不是唯一能改变上游地址的凭证字段：OpenAI 系列的多协议账号
// （kimi/zhipu/deepseek/minimax 等 adaptive 模式）会优先使用 api_base_urls 中
// 的分协议地址（见 Account.GetOpenAIBaseURL）。只校验 base_url 会留下绕过路径，
// 所以这里把所有可能生效的地址字段统一检查。
func (s *AccountContributionService) checkExpectedBaseURL(task *AccountTask, credentials map[string]any) error {
	if task.ExpectedBaseURL == "" {
		return nil
	}
	hosts := collectCredentialBaseURLHosts(credentials)
	if len(hosts) == 0 {
		// 任务限定了上游时必须能确认地址；缺失视为不匹配，避免绕过约束。
		return ErrAccountTaskBaseURL
	}
	for _, host := range hosts {
		if !strings.EqualFold(host, task.ExpectedBaseURL) {
			return ErrAccountTaskBaseURL
		}
	}
	return nil
}

// collectCredentialBaseURLHosts 汇总凭证中所有可能生效的上游主机。
func collectCredentialBaseURLHosts(credentials map[string]any) []string {
	hosts := make([]string, 0, 4)
	if raw, ok := credentials["base_url"].(string); ok {
		if host := extractBaseURLHost(raw); host != "" {
			hosts = append(hosts, host)
		}
	}
	// api_base_urls: {protocol: url}，adaptive 账号按协议取用。
	if nested, ok := credentials["api_base_urls"].(map[string]any); ok {
		keys := make([]string, 0, len(nested))
		for key := range nested {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if raw, ok := nested[key].(string); ok {
				if host := extractBaseURLHost(raw); host != "" {
					hosts = append(hosts, host)
				}
			}
		}
	}
	return hosts
}

// verifyCredential 用临时账号拉取一次上游模型列表，验证凭证可用。
// 账号未入库时不走 token 刷新路径，因此 OAuth 凭证必须自带未过期的 access_token。
func (s *AccountContributionService) verifyCredential(ctx context.Context, task *AccountTask, in *AccountSubmissionCreateInput) (string, string, int) {
	if !task.VerifyModels {
		return AccountVerifyNotRun, "", 0
	}
	probe := &Account{
		Platform:    in.Platform,
		Type:        in.Type,
		Credentials: in.Credentials,
		Extra:       map[string]any{},
		Status:      StatusActive,
	}
	// 凭证存在性检查不依赖探测服务，先做掉：没有密钥的提交无论配置如何都无效。
	if probe.IsReverse() {
		// 反代适配器只读凭证字段，不需要入库账号。
	} else if strings.TrimSpace(probe.GetCredential("api_key")) == "" && strings.TrimSpace(probe.GetCredential("access_token")) == "" {
		return AccountVerifyFailed, "no api_key or access_token in credentials", 0
	}
	if s.testService == nil {
		return AccountVerifyNotRun, "", 0
	}

	// 用户提交的 base_url 由外部控制，探测时强制公共主机校验，
	// 防止把本服务当作内网探测通道。
	probeCtx := WithHTTPUpstreamPublicHostsOnly(ctx)
	probeCtx, cancel := context.WithTimeout(probeCtx, accountContributionProbeTimeout)
	defer cancel()

	models, err := s.testService.FetchUpstreamSupportedModels(probeCtx, probe)
	if err != nil {
		msg := err.Error()
		if safe, ok := err.(interface{ SafeMessage() string }); ok {
			msg = safe.SafeMessage()
		}
		return AccountVerifyFailed, msg, 0
	}
	if len(models) == 0 {
		return AccountVerifyFailed, "upstream returned no models", 0
	}
	return AccountVerifyPassed, "", len(models)
}

// ---------- 审核 ----------

// ListPendingSubmissions 列出待审提交。
func (s *AccountContributionService) ListPendingSubmissions(ctx context.Context, taskID int64) ([]*AccountSubmission, error) {
	return s.repo.ListPendingSubmissions(ctx, taskID)
}

// ListUserSubmissions 列出某用户的提交。
func (s *AccountContributionService) ListUserSubmissions(ctx context.Context, userID int64) ([]*AccountSubmission, error) {
	return s.repo.ListSubmissionsByUser(ctx, userID)
}

// ApproveInput 审核通过输入。
type ApproveInput struct {
	SubmissionID int64
	ReviewerID   int64
	Note         string
	GrantReward  bool
}

// Approve 审核通过：激活账号、绑定分组、发放一次性奖励。
//
// 顺序刻意做成「先落审核状态，再发奖励」：账号激活等步骤失败时提交仍是 pending，
// 管理员可以重试；而奖励发放失败不会把整条审核卡在中间态——状态已经是 approved，
// 账号也可用，奖励可由管理员在订阅管理里手工补发。反过来（先发奖励再落状态）一旦
// 落状态失败，重复点击会重复发放。
func (s *AccountContributionService) Approve(ctx context.Context, in *ApproveInput) (*AccountSubmission, error) {
	sub, err := s.repo.GetSubmissionByID(ctx, in.SubmissionID)
	if err != nil {
		return nil, err
	}
	if sub == nil {
		return nil, ErrAccountSubmissionNotFound
	}
	if sub.Status != AccountSubmissionPending {
		return nil, ErrAccountSubmissionState
	}
	task, err := s.GetTask(ctx, sub.TaskID)
	if err != nil {
		return nil, err
	}

	// 1) 激活账号并绑定分组。任一步失败都保持 pending，允许重试。
	if _, err := s.adminService.SetAccountSchedulable(ctx, sub.AccountID, true); err != nil {
		return nil, err
	}
	if _, err := s.adminService.UpdateAccount(ctx, sub.AccountID, &UpdateAccountInput{Status: StatusActive}); err != nil {
		return nil, err
	}
	if len(task.TargetGroupIDs) > 0 {
		groupIDs := append([]int64(nil), task.TargetGroupIDs...)
		if _, err := s.adminService.UpdateAccount(ctx, sub.AccountID, &UpdateAccountInput{
			GroupIDs:              &groupIDs,
			SkipMixedChannelCheck: true,
		}); err != nil {
			return nil, err
		}
	}

	// 2) 落审核状态。此后重复提交审核会被状态检查拦住，不会二次发奖。
	now := time.Now().UTC()
	sub.Status = AccountSubmissionApproved
	sub.ReviewedBy = &in.ReviewerID
	sub.ReviewedAt = &now
	sub.ReviewNote = in.Note

	// 3) 发奖励。失败只记录，不回滚审核结果。
	if in.GrantReward && sub.RewardGrantedAt == nil {
		granted, grantErr := s.grantReward(ctx, task, sub, in.ReviewerID)
		if grantErr != nil {
			logger.LegacyPrintf("service.account_contribution",
				"grant reward failed: submission=%d user=%d task=%d err=%v",
				sub.ID, sub.UserID, task.ID, grantErr)
		} else if granted != nil {
			sub.RewardSubscriptionID = &granted.ID
			sub.RewardGrantedAt = &now
		}
	}

	if err := s.repo.UpdateSubmission(ctx, sub); err != nil {
		return nil, err
	}
	return sub, nil
}

// grantReward 发放一次性订阅奖励；已有奖励记录时返回 nil。
func (s *AccountContributionService) grantReward(ctx context.Context, task *AccountTask, sub *AccountSubmission, reviewerID int64) (*UserSubscription, error) {
	if s.subscriptionSvc == nil {
		return nil, errors.New("subscription service is not configured")
	}
	has, err := s.repo.HasUserReward(ctx, task.ID, sub.UserID)
	if err != nil {
		return nil, err
	}
	if has {
		return nil, nil
	}
	return s.subscriptionSvc.AssignSubscription(ctx, &AssignSubscriptionInput{
		UserID:       sub.UserID,
		GroupID:      task.RewardGroupID,
		ValidityDays: task.RewardDays,
		AssignedBy:   reviewerID,
		Notes:        fmt.Sprintf("account contribution reward (task %d, submission %d)", task.ID, sub.ID),
	})
}

// Reject 审核拒绝：软删账号并清空凭证。
func (s *AccountContributionService) Reject(ctx context.Context, submissionID, reviewerID int64, note string) (*AccountSubmission, error) {
	sub, err := s.repo.GetSubmissionByID(ctx, submissionID)
	if err != nil {
		return nil, err
	}
	if sub == nil {
		return nil, ErrAccountSubmissionNotFound
	}
	if sub.Status != AccountSubmissionPending {
		return nil, ErrAccountSubmissionState
	}
	// 拒绝流程：先清空凭证，再软删账号。
	// 清凭证必须走 accountRepo.UpdateCredentials 直写——UpdateAccount 对敏感键
	// 采用"incoming 没提供就保留"的合并语义（给前端脱敏编辑用的），走它清不掉。
	// 账号删除是软删（SoftDeleteMixin 拦截 DELETE 改写成 deleted_at），因此
	// account_submissions.account_id 的外键不会失效。
	if updater, ok := any(s.accountRepo).(accountCredentialsUpdater); ok {
		if err := updater.UpdateCredentials(ctx, sub.AccountID, map[string]any{}); err != nil {
			return nil, fmt.Errorf("clear rejected credentials: %w", err)
		}
	}
	if err := s.adminService.DeleteAccount(ctx, sub.AccountID); err != nil {
		return nil, fmt.Errorf("delete rejected account: %w", err)
	}

	now := time.Now().UTC()
	sub.Status = AccountSubmissionRejected
	sub.ReviewedBy = &reviewerID
	sub.ReviewedAt = &now
	sub.ReviewNote = note
	if err := s.repo.UpdateSubmission(ctx, sub); err != nil {
		return nil, err
	}
	return sub, nil
}

// ---------- 工具函数 ----------

// credentialFingerprint 计算凭证摘要，用于拒绝重复提交。
// 只覆盖稳定字段，避免时间戳类字段导致同一凭证算出不同指纹。
func credentialFingerprint(platform, accountType string, credentials map[string]any) string {
	keys := make([]string, 0, len(credentials))
	for key := range credentials {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	payload := map[string]any{}
	for _, key := range keys {
		lower := strings.ToLower(key)
		if strings.HasSuffix(lower, "_at") || strings.HasSuffix(lower, "_time") || lower == "expires_at" || lower == "created_at" {
			continue
		}
		payload[key] = credentials[key]
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(append([]byte(platform+"\x00"+accountType+"\x00"), encoded...))
	return hex.EncodeToString(sum[:])
}

func normalizeStringList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func normalizeInt64List(values []int64) []int64 {
	out := make([]int64, 0, len(values))
	seen := make(map[int64]struct{}, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// normalizeBaseURLHost 把配置值归一成小写主机名；无法解析时原样保留小写化结果。
func normalizeBaseURLHost(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Hostname() != "" {
		return strings.ToLower(parsed.Hostname())
	}
	return strings.ToLower(trimmed)
}

func extractBaseURLHost(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Hostname() != "" {
		return strings.ToLower(parsed.Hostname())
	}
	return ""
}

// ValidateExpectedBaseURL 供 handler 预校验使用。
func ValidateExpectedBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	normalized, err := urlvalidator.ValidateURLFormat(trimmed, true)
	if err != nil {
		return "", err
	}
	return normalized, nil
}

package admin

import (
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// AccountTaskHandler 管理账号贡献任务与提交审核。
type AccountTaskHandler struct {
	svc *service.AccountContributionService
}

// NewAccountTaskHandler 构造管理端 handler。
func NewAccountTaskHandler(svc *service.AccountContributionService) *AccountTaskHandler {
	return &AccountTaskHandler{svc: svc}
}

// AccountTaskAdminDTO 是管理端任务视图，包含全部配置。
type AccountTaskAdminDTO struct {
	ID                 int64    `json:"id"`
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	Enabled            bool     `json:"enabled"`
	Platform           string   `json:"platform"`
	AccountTypes       []string `json:"account_types"`
	TargetGroupIDs     []int64  `json:"target_group_ids"`
	RewardGroupID      int64    `json:"reward_group_id"`
	RewardDays         int      `json:"reward_days"`
	MaxPerUser         int      `json:"max_per_user"`
	ExpectedBaseURL    string   `json:"expected_base_url"`
	VerifyModels       bool     `json:"verify_models"`
	RejectOnVerifyFail bool     `json:"reject_on_verify_fail"`
	Priority           int      `json:"priority"`
	Concurrency        int      `json:"concurrency"`
	RateMultiplier     *float64 `json:"rate_multiplier,omitempty"`
	SortOrder          int      `json:"sort_order"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
}

// AccountSubmissionAdminDTO 是管理端提交视图。
type AccountSubmissionAdminDTO struct {
	ID                   int64   `json:"id"`
	TaskID               int64   `json:"task_id"`
	UserID               int64   `json:"user_id"`
	AccountID            int64   `json:"account_id"`
	Status               string  `json:"status"`
	VerificationStatus   string  `json:"verification_status"`
	VerificationError    string  `json:"verification_error,omitempty"`
	ModelCount           int     `json:"model_count"`
	ReviewedBy           *int64  `json:"reviewed_by,omitempty"`
	ReviewNote           string  `json:"review_note,omitempty"`
	RewardSubscriptionID *int64  `json:"reward_subscription_id,omitempty"`
	RewardGrantedAt      *string `json:"reward_granted_at,omitempty"`
	CreatedAt            string  `json:"created_at"`
}

func toAccountTaskAdminDTO(task *service.AccountTask) AccountTaskAdminDTO {
	return AccountTaskAdminDTO{
		ID:                 task.ID,
		Name:               task.Name,
		Description:        task.Description,
		Enabled:            task.Enabled,
		Platform:           task.Platform,
		AccountTypes:       task.AccountTypes,
		TargetGroupIDs:     task.TargetGroupIDs,
		RewardGroupID:      task.RewardGroupID,
		RewardDays:         task.RewardDays,
		MaxPerUser:         task.MaxPerUser,
		ExpectedBaseURL:    task.ExpectedBaseURL,
		VerifyModels:       task.VerifyModels,
		RejectOnVerifyFail: task.RejectOnVerifyFail,
		Priority:           task.Priority,
		Concurrency:        task.Concurrency,
		RateMultiplier:     task.RateMultiplier,
		SortOrder:          task.SortOrder,
		CreatedAt:          task.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:          task.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func toAccountSubmissionAdminDTO(sub *service.AccountSubmission) AccountSubmissionAdminDTO {
	dto := AccountSubmissionAdminDTO{
		ID:                   sub.ID,
		TaskID:               sub.TaskID,
		UserID:               sub.UserID,
		AccountID:            sub.AccountID,
		Status:               sub.Status,
		VerificationStatus:   sub.VerificationStatus,
		VerificationError:    sub.VerificationError,
		ModelCount:           sub.ModelCount,
		ReviewedBy:           sub.ReviewedBy,
		ReviewNote:           sub.ReviewNote,
		RewardSubscriptionID: sub.RewardSubscriptionID,
		CreatedAt:            sub.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if sub.RewardGrantedAt != nil {
		formatted := sub.RewardGrantedAt.Format("2006-01-02T15:04:05Z07:00")
		dto.RewardGrantedAt = &formatted
	}
	return dto
}

// AccountTaskRequest 是创建/更新任务的请求体。
type AccountTaskRequest struct {
	Name                *string   `json:"name"`
	Description         *string   `json:"description"`
	Enabled             *bool     `json:"enabled"`
	Platform            *string   `json:"platform"`
	AccountTypes        *[]string `json:"account_types"`
	TargetGroupIDs      *[]int64  `json:"target_group_ids"`
	RewardGroupID       *int64    `json:"reward_group_id"`
	RewardDays          *int      `json:"reward_days"`
	MaxPerUser          *int      `json:"max_per_user"`
	ExpectedBaseURL     *string   `json:"expected_base_url"`
	VerifyModels        *bool     `json:"verify_models"`
	RejectOnVerifyFail  *bool     `json:"reject_on_verify_fail"`
	Priority            *int      `json:"priority"`
	Concurrency         *int      `json:"concurrency"`
	RateMultiplier      *float64  `json:"rate_multiplier"`
	ClearRateMultiplier bool      `json:"clear_rate_multiplier"`
	SortOrder           *int      `json:"sort_order"`
}

// ListTasks 列出全部任务。
// GET /api/v1/admin/account-tasks
func (h *AccountTaskHandler) ListTasks(c *gin.Context) {
	tasks, err := h.svc.ListTasks(c.Request.Context(), false)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	result := make([]AccountTaskAdminDTO, 0, len(tasks))
	for _, task := range tasks {
		result = append(result, toAccountTaskAdminDTO(task))
	}
	response.Success(c, result)
}

// GetTask 读取单个任务。
// GET /api/v1/admin/account-tasks/:id
func (h *AccountTaskHandler) GetTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid task ID")
		return
	}
	task, err := h.svc.GetTask(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, toAccountTaskAdminDTO(task))
}

// CreateTask 创建任务。
// POST /api/v1/admin/account-tasks
func (h *AccountTaskHandler) CreateTask(c *gin.Context) {
	var req AccountTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	input := &service.AccountTaskCreateInput{
		MaxPerUser:         1,
		VerifyModels:       true,
		RejectOnVerifyFail: true,
		Priority:           50,
		Concurrency:        3,
		Enabled:            true,
	}
	if req.Name != nil {
		input.Name = *req.Name
	}
	if req.Description != nil {
		input.Description = *req.Description
	}
	if req.Enabled != nil {
		input.Enabled = *req.Enabled
	}
	if req.Platform != nil {
		input.Platform = *req.Platform
	}
	if req.AccountTypes != nil {
		input.AccountTypes = *req.AccountTypes
	}
	if req.TargetGroupIDs != nil {
		input.TargetGroupIDs = *req.TargetGroupIDs
	}
	if req.RewardGroupID != nil {
		input.RewardGroupID = *req.RewardGroupID
	}
	if req.RewardDays != nil {
		input.RewardDays = *req.RewardDays
	}
	if req.MaxPerUser != nil {
		input.MaxPerUser = *req.MaxPerUser
	}
	if req.ExpectedBaseURL != nil {
		input.ExpectedBaseURL = *req.ExpectedBaseURL
	}
	if req.VerifyModels != nil {
		input.VerifyModels = *req.VerifyModels
	}
	if req.RejectOnVerifyFail != nil {
		input.RejectOnVerifyFail = *req.RejectOnVerifyFail
	}
	if req.Priority != nil {
		input.Priority = *req.Priority
	}
	if req.Concurrency != nil {
		input.Concurrency = *req.Concurrency
	}
	if req.RateMultiplier != nil {
		input.RateMultiplier = req.RateMultiplier
	}
	if req.SortOrder != nil {
		input.SortOrder = *req.SortOrder
	}
	task, err := h.svc.CreateTask(c.Request.Context(), input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"code": 0, "message": "success", "data": toAccountTaskAdminDTO(task)})
}

// UpdateTask 更新任务。
// PUT /api/v1/admin/account-tasks/:id
func (h *AccountTaskHandler) UpdateTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid task ID")
		return
	}
	var req AccountTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	task, err := h.svc.UpdateTask(c.Request.Context(), id, &service.AccountTaskUpdateInput{
		Name:                req.Name,
		Description:         req.Description,
		Platform:            req.Platform,
		AccountTypes:        req.AccountTypes,
		TargetGroupIDs:      req.TargetGroupIDs,
		RewardGroupID:       req.RewardGroupID,
		RewardDays:          req.RewardDays,
		MaxPerUser:          req.MaxPerUser,
		ExpectedBaseURL:     req.ExpectedBaseURL,
		VerifyModels:        req.VerifyModels,
		RejectOnVerifyFail:  req.RejectOnVerifyFail,
		Priority:            req.Priority,
		Concurrency:         req.Concurrency,
		RateMultiplier:      req.RateMultiplier,
		ClearRateMultiplier: req.ClearRateMultiplier,
		SortOrder:           req.SortOrder,
		Enabled:             req.Enabled,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, toAccountTaskAdminDTO(task))
}

// DeleteTask 删除任务。
// DELETE /api/v1/admin/account-tasks/:id
func (h *AccountTaskHandler) DeleteTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid task ID")
		return
	}
	if err := h.svc.DeleteTask(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

// ListSubmissions 列出待审提交；task_id 可选。
// GET /api/v1/admin/account-submissions
func (h *AccountTaskHandler) ListSubmissions(c *gin.Context) {
	var taskID int64
	if raw := c.Query("task_id"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			response.BadRequest(c, "Invalid task ID")
			return
		}
		taskID = parsed
	}
	subs, err := h.svc.ListPendingSubmissions(c.Request.Context(), taskID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	result := make([]AccountSubmissionAdminDTO, 0, len(subs))
	for _, sub := range subs {
		result = append(result, toAccountSubmissionAdminDTO(sub))
	}
	response.Success(c, result)
}

// ApproveRequest 是审核通过的请求体。
type ApproveRequest struct {
	Note        string `json:"note"`
	GrantReward *bool  `json:"grant_reward"`
}

// Approve 审核通过。
// POST /api/v1/admin/account-submissions/:id/approve
func (h *AccountTaskHandler) Approve(c *gin.Context) {
	subID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid submission ID")
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Authentication required")
		return
	}
	var req ApproveRequest
	_ = c.ShouldBindJSON(&req)
	grantReward := true
	if req.GrantReward != nil {
		grantReward = *req.GrantReward
	}
	sub, err := h.svc.Approve(c.Request.Context(), &service.ApproveInput{
		SubmissionID: subID,
		ReviewerID:   subject.UserID,
		Note:         req.Note,
		GrantReward:  grantReward,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, toAccountSubmissionAdminDTO(sub))
}

// RejectRequest 是审核拒绝的请求体。
type RejectRequest struct {
	Note string `json:"note"`
}

// Reject 审核拒绝。
// POST /api/v1/admin/account-submissions/:id/reject
func (h *AccountTaskHandler) Reject(c *gin.Context) {
	subID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid submission ID")
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Authentication required")
		return
	}
	var req RejectRequest
	_ = c.ShouldBindJSON(&req)
	sub, err := h.svc.Reject(c.Request.Context(), subID, subject.UserID, req.Note)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, toAccountSubmissionAdminDTO(sub))
}

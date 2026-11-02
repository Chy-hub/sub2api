package handler

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// AccountContributionHandler 处理用户侧的账号贡献任务与提交。
type AccountContributionHandler struct {
	svc *service.AccountContributionService
}

// NewAccountContributionHandler 构造用户侧 handler。
func NewAccountContributionHandler(svc *service.AccountContributionService) *AccountContributionHandler {
	return &AccountContributionHandler{svc: svc}
}

// AccountTaskDTO 是面向用户的公开任务视图，不暴露内部分组与优先级配置。
type AccountTaskDTO struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Platform      string   `json:"platform"`
	AccountTypes  []string `json:"account_types"`
	RewardDays    int      `json:"reward_days"`
	MaxPerUser    int      `json:"max_per_user"`
	VerifyModels  bool     `json:"verify_models"`
	RewardGroupID int64    `json:"reward_group_id"`
	// ExpectedBaseURL 非空时表示任务已锁定上游，前端据此预填并隐藏输入框。
	ExpectedBaseURL string `json:"expected_base_url,omitempty"`
}

// AccountSubmissionDTO 是用户可见的提交记录。
type AccountSubmissionDTO struct {
	ID                   int64   `json:"id"`
	TaskID               int64   `json:"task_id"`
	AccountID            int64   `json:"account_id"`
	Status               string  `json:"status"`
	VerificationStatus   string  `json:"verification_status"`
	VerificationError    string  `json:"verification_error,omitempty"`
	ModelCount           int     `json:"model_count"`
	ReviewNote           string  `json:"review_note,omitempty"`
	RewardGrantedAt      *string `json:"reward_granted_at,omitempty"`
	CreatedAt            string  `json:"created_at"`
}

func toAccountTaskDTO(task *service.AccountTask) AccountTaskDTO {
	return AccountTaskDTO{
		ID:            task.ID,
		Name:          task.Name,
		Description:   task.Description,
		Platform:      task.Platform,
		AccountTypes:  task.AccountTypes,
		RewardDays:    task.RewardDays,
		MaxPerUser:    task.MaxPerUser,
		VerifyModels:    task.VerifyModels,
		RewardGroupID:   task.RewardGroupID,
		ExpectedBaseURL: task.ExpectedBaseURL,
	}
}

func toAccountSubmissionDTO(sub *service.AccountSubmission) AccountSubmissionDTO {
	dto := AccountSubmissionDTO{
		ID:                 sub.ID,
		TaskID:             sub.TaskID,
		AccountID:          sub.AccountID,
		Status:             sub.Status,
		VerificationStatus: sub.VerificationStatus,
		VerificationError:  sub.VerificationError,
		ModelCount:         sub.ModelCount,
		ReviewNote:         sub.ReviewNote,
		CreatedAt:          sub.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if sub.RewardGrantedAt != nil {
		formatted := sub.RewardGrantedAt.Format("2006-01-02T15:04:05Z07:00")
		dto.RewardGrantedAt = &formatted
	}
	return dto
}

// ListTasks 列出当前可提交的任务。
// GET /api/v1/user/account-tasks
func (h *AccountContributionHandler) ListTasks(c *gin.Context) {
	tasks, err := h.svc.ListTasks(c.Request.Context(), true)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	result := make([]AccountTaskDTO, 0, len(tasks))
	for _, task := range tasks {
		result = append(result, toAccountTaskDTO(task))
	}
	response.Success(c, result)
}

// SubmitRequest 是用户提交账号的请求体。
type SubmitRequest struct {
	TaskID      int64          `json:"task_id" binding:"required"`
	Name        string         `json:"name"`
	Platform    string         `json:"platform" binding:"required"`
	Type        string         `json:"type" binding:"required"`
	Credentials map[string]any `json:"credentials" binding:"required"`
}

// Submit 提交账号供审核。
// POST /api/v1/user/account-submissions
func (h *AccountContributionHandler) Submit(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	var req SubmitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	sub, err := h.svc.Submit(c.Request.Context(), &service.AccountSubmissionCreateInput{
		TaskID:      req.TaskID,
		UserID:      subject.UserID,
		Name:        req.Name,
		Platform:    req.Platform,
		Type:        req.Type,
		Credentials: req.Credentials,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"code": 0, "message": "success", "data": toAccountSubmissionDTO(sub)})
}

// ListSubmissions 列出当前用户的提交记录。
// GET /api/v1/user/account-submissions
func (h *AccountContributionHandler) ListSubmissions(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	subs, err := h.svc.ListUserSubmissions(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	result := make([]AccountSubmissionDTO, 0, len(subs))
	for _, sub := range subs {
		result = append(result, toAccountSubmissionDTO(sub))
	}
	response.Success(c, result)
}

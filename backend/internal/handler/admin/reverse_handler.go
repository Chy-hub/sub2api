package admin

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *AccountHandler) reverseOperationAccount(c *gin.Context) (*service.Account, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return nil, false
	}
	if h.reverseGateway == nil {
		response.ErrorFrom(c, service.ErrReverseGatewayUnavailable)
		return nil, false
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), id)
	if err != nil {
		response.NotFound(c, "Account not found")
		return nil, false
	}
	if !account.IsReverse() {
		response.BadRequest(c, "Account is not a reverse account")
		return nil, false
	}
	return account, true
}

func (h *AccountHandler) ReverseQuota(c *gin.Context) {
	account, ok := h.reverseOperationAccount(c)
	if !ok {
		return
	}
	quota, err := h.reverseGateway.Quota(c.Request.Context(), account)
	if err != nil {
		response.Error(c, http.StatusBadGateway, err.Error())
		return
	}
	response.Success(c, quota)
}

func (h *AccountHandler) ReverseTasks(c *gin.Context) {
	account, ok := h.reverseOperationAccount(c)
	if !ok {
		return
	}
	if !service.IsReverseProvider(account.Platform, "workbuddy") {
		response.BadRequest(c, "This account has no growth tasks")
		return
	}
	result, err := h.reverseGateway.TaskOverview(c.Request.Context(), account)
	if err != nil {
		response.Error(c, http.StatusBadGateway, err.Error())
		return
	}
	response.Success(c, result)
}

func (h *AccountHandler) RunReverseTask(c *gin.Context) {
	account, ok := h.reverseOperationAccount(c)
	if !ok {
		return
	}
	if !service.IsReverseProvider(account.Platform, "workbuddy") {
		response.BadRequest(c, "This account has no growth tasks")
		return
	}
	var req service.ReverseTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid task request: "+err.Error())
		return
	}
	result, err := h.reverseGateway.RunTask(c.Request.Context(), account, req)
	if errors.Is(err, service.ErrReverseOperationBusy) {
		response.Error(c, http.StatusConflict, err.Error())
		return
	}
	if err != nil && result == nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, result)
}

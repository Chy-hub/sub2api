package admin

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// reverseLoginActorID returns the authenticated administrator that will own a
// reverse browser-login session or ticket.
func reverseLoginActorID(c *gin.Context) (int64, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Error(c, http.StatusUnauthorized, "Authentication required")
		return 0, false
	}
	return subject.UserID, true
}

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

// ReverseModelCatalog returns the account's upstream model catalogue together
// with the parsing evidence: which upstream categories existed, which were
// read, and every entry that was dropped with a reason code.
//
// It answers the question the plain model listing cannot: when a model is
// visible in the vendor's own client but absent from the gateway, this endpoint
// shows whether the upstream sent it and the parser dropped it, or whether it
// never arrived at all.
func (h *AccountHandler) ReverseModelCatalog(c *gin.Context) {
	account, ok := h.reverseOperationAccount(c)
	if !ok {
		return
	}
	catalog, err := h.reverseGateway.ModelCatalog(c.Request.Context(), account)
	if err != nil {
		response.Error(c, http.StatusBadGateway, err.Error())
		return
	}
	response.Success(c, catalog)
}

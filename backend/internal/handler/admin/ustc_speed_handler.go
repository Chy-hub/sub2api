package admin

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	ustcSpeedDefaultTimezone = "Asia/Shanghai"
	ustcSpeedMaxRangeDays    = 180
	ustcSpeedDefaultLimit    = 50
	ustcSpeedMaxLimit        = 200
	ustcSpeedMaxAccounts     = 100
)

// USTCSpeedHandler serves the admin API for USTC speed observations.
type USTCSpeedHandler struct {
	speedService *service.USTCSpeedService
}

func NewUSTCSpeedHandler(speedService *service.USTCSpeedService) *USTCSpeedHandler {
	return &USTCSpeedHandler{speedService: speedService}
}

func (h *USTCSpeedHandler) Dimensions(c *gin.Context) {
	if !h.requireService(c) {
		return
	}
	out, err := h.speedService.Dimensions(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}

func (h *USTCSpeedHandler) Overview(c *gin.Context) {
	if !h.requireService(c) {
		return
	}
	query, err := parseUSTCSpeedQuery(c, time.Now())
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	out, err := h.speedService.Overview(c.Request.Context(), query)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, out)
}

func (h *USTCSpeedHandler) Samples(c *gin.Context) {
	if !h.requireService(c) {
		return
	}
	query, err := parseUSTCSpeedQuery(c, time.Now())
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	items, total, err := h.speedService.ListSamples(c.Request.Context(), query)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if items == nil {
		items = []service.USTCSpeedSample{}
	}
	response.Success(c, gin.H{"items": items, "total": total})
}

func (h *USTCSpeedHandler) Sample(c *gin.Context) {
	if !h.requireService(c) {
		return
	}
	id, ok := ustcSpeedPathID(c)
	if !ok {
		return
	}
	item, err := h.speedService.GetSample(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

func (h *USTCSpeedHandler) ListTasks(c *gin.Context) {
	if !h.requireService(c) {
		return
	}
	items, err := h.speedService.ListTasks(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if items == nil {
		items = []service.USTCSpeedTask{}
	}
	response.Success(c, gin.H{"items": items})
}

func (h *USTCSpeedHandler) CreateTask(c *gin.Context) {
	if !h.requireService(c) {
		return
	}
	task := service.DefaultUSTCSpeedTask()
	if err := c.ShouldBindJSON(&task); err != nil {
		response.BadRequest(c, "Invalid task: "+err.Error())
		return
	}
	task.ID = 0
	if err := h.speedService.SaveTask(c.Request.Context(), &task); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, task)
}

func (h *USTCSpeedHandler) UpdateTask(c *gin.Context) {
	if !h.requireService(c) {
		return
	}
	id, ok := ustcSpeedPathID(c)
	if !ok {
		return
	}
	task, err := h.speedService.GetTask(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if task == nil {
		response.NotFound(c, "Speed task not found")
		return
	}
	if err := c.ShouldBindJSON(task); err != nil {
		response.BadRequest(c, "Invalid task: "+err.Error())
		return
	}
	task.ID = id
	if err := h.speedService.SaveTask(c.Request.Context(), task); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, task)
}

func (h *USTCSpeedHandler) DeleteTask(c *gin.Context) {
	if !h.requireService(c) {
		return
	}
	id, ok := ustcSpeedPathID(c)
	if !ok {
		return
	}
	if task, err := h.speedService.GetTask(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	} else if task == nil {
		response.NotFound(c, "Speed task not found")
		return
	}
	if err := h.speedService.DeleteTask(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "deleted"})
}

func (h *USTCSpeedHandler) RunTask(c *gin.Context) {
	if !h.requireService(c) {
		return
	}
	id, ok := ustcSpeedPathID(c)
	if !ok {
		return
	}
	roundID, err := h.speedService.RunTask(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Accepted(c, gin.H{"round_id": roundID, "status": "queued"})
}

func (h *USTCSpeedHandler) GetSettings(c *gin.Context) {
	if !h.requireService(c) {
		return
	}
	settings, err := h.speedService.GetSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

func (h *USTCSpeedHandler) SaveSettings(c *gin.Context) {
	if !h.requireService(c) {
		return
	}
	settings, err := h.speedService.GetSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if settings == nil {
		defaultSettings := service.DefaultUSTCSpeedSettings()
		settings = &defaultSettings
	}
	if err := c.ShouldBindJSON(settings); err != nil {
		response.BadRequest(c, "Invalid settings: "+err.Error())
		return
	}
	if err := h.speedService.SaveSettings(c.Request.Context(), *settings); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

func (h *USTCSpeedHandler) Runtime(c *gin.Context) {
	if !h.requireService(c) {
		return
	}
	items, err := h.speedService.Runtime(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if items == nil {
		items = []service.USTCSpeedRuntime{}
	}
	response.Success(c, gin.H{"items": items})
}

func (h *USTCSpeedHandler) requireService(c *gin.Context) bool {
	if h == nil || h.speedService == nil {
		response.Error(c, http.StatusServiceUnavailable, "USTC speed service not available")
		return false
	}
	return true
}

func ustcSpeedPathID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid speed resource ID")
		return 0, false
	}
	return id, true
}

// parseUSTCSpeedQuery validates and converts request filters to the service's
// half-open time range. The supplied clock makes default-date handling testable.
func parseUSTCSpeedQuery(c *gin.Context, now time.Time) (service.USTCSpeedQuery, error) {
	var query service.USTCSpeedQuery

	timezone := strings.TrimSpace(c.Query("timezone"))
	if timezone == "" {
		timezone = ustcSpeedDefaultTimezone
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return query, errors.New("invalid timezone")
	}

	today := now.In(location).Format("2006-01-02")
	startDate := strings.TrimSpace(c.Query("start_date"))
	endDate := strings.TrimSpace(c.Query("end_date"))
	if startDate == "" {
		startDate = today
	}
	if endDate == "" {
		endDate = today
	}
	start, err := time.ParseInLocation("2006-01-02", startDate, location)
	if err != nil {
		return query, errors.New("invalid start_date; expected YYYY-MM-DD")
	}
	endDateTime, err := time.ParseInLocation("2006-01-02", endDate, location)
	if err != nil {
		return query, errors.New("invalid end_date; expected YYYY-MM-DD")
	}
	if endDateTime.Before(start) {
		return query, errors.New("start_date must be on or before end_date")
	}
	calendarStart := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	calendarEnd := time.Date(endDateTime.Year(), endDateTime.Month(), endDateTime.Day(), 0, 0, 0, 0, time.UTC)
	calendarDays := int(calendarEnd.Sub(calendarStart)/(24*time.Hour)) + 1
	if calendarDays > ustcSpeedMaxRangeDays {
		return query, fmt.Errorf("date range cannot exceed %d days", ustcSpeedMaxRangeDays)
	}
	end := endDateTime.AddDate(0, 0, 1)

	model := strings.TrimSpace(c.Query("model"))
	if len(model) > 200 {
		return query, errors.New("model is too long")
	}
	profile := strings.TrimSpace(c.Query("profile"))
	if len(profile) > 128 {
		return query, errors.New("profile is too long")
	}
	groupBy := strings.TrimSpace(c.Query("group_by"))
	if groupBy == "" {
		groupBy = "model"
	}
	if !oneOf(groupBy, "model", "profile") {
		return query, errors.New("group_by must be model or profile")
	}
	source := strings.TrimSpace(c.Query("source"))
	if source == "" {
		source = "real"
	}
	if source != "real" && source != "probe" && source != "all" {
		return query, errors.New("source must be real, probe or all")
	}
	metric := strings.TrimSpace(c.Query("metric"))
	if metric == "" {
		metric = "generation_tps"
	}
	if !oneOf(metric, "generation_tps", "overall_tps", "ttft_ms", "first_output_ms", "total_ms", "output_ms", "headers_ms", "tail_ms", "max_gap_ms") {
		return query, errors.New("invalid metric")
	}
	interval := strings.TrimSpace(c.Query("interval"))
	if interval == "" {
		interval = "auto"
	}
	if !oneOf(interval, "raw", "1m", "5m", "15m", "1h", "1d", "auto") {
		return query, errors.New("invalid interval")
	}
	aggregate := strings.TrimSpace(c.Query("aggregate"))
	if aggregate == "" {
		aggregate = "mean"
	}
	if !oneOf(aggregate, "p50", "mean", "p10", "p90") {
		return query, errors.New("invalid aggregate")
	}
	status := strings.TrimSpace(c.Query("status"))
	if status != "" && !oneOf(status, "success", "error", "rate_limited", "skipped", "cancelled", "recorded") {
		return query, errors.New("invalid status")
	}
	reasoning := strings.TrimSpace(c.Query("reasoning"))
	if reasoning == "" {
		reasoning = "all"
	}
	if !oneOf(reasoning, "all", "on", "off", "unknown") {
		return query, errors.New("reasoning must be all, on, off or unknown")
	}
	stream := strings.TrimSpace(c.Query("stream"))
	if stream == "" {
		stream = "all"
	}
	if !oneOf(stream, "all", "true", "false") {
		return query, errors.New("stream must be all, true or false")
	}

	accountIDs, err := parseUSTCSpeedAccountIDs(c.Query("account_ids"))
	if err != nil {
		return query, err
	}
	taskID, err := parseOptionalPositiveInt64(c.Query("task_id"), "task_id")
	if err != nil {
		return query, err
	}
	inputMin, err := parseOptionalNonNegativeInt(c.Query("input_min"), "input_min")
	if err != nil {
		return query, err
	}
	inputMax, err := parseOptionalNonNegativeInt(c.Query("input_max"), "input_max")
	if err != nil {
		return query, err
	}
	outputMin, err := parseOptionalNonNegativeInt(c.Query("output_min"), "output_min")
	if err != nil {
		return query, err
	}
	outputMax, err := parseOptionalNonNegativeInt(c.Query("output_max"), "output_max")
	if err != nil {
		return query, err
	}
	if inputMin != nil && inputMax != nil && *inputMin > *inputMax {
		return query, errors.New("input_min cannot exceed input_max")
	}
	if outputMin != nil && outputMax != nil && *outputMin > *outputMax {
		return query, errors.New("output_min cannot exceed output_max")
	}
	limit, err := parseQueryInt(c, "limit", ustcSpeedDefaultLimit)
	if err != nil {
		return query, err
	}
	if limit < 1 || limit > ustcSpeedMaxLimit {
		return query, fmt.Errorf("limit must be between 1 and %d", ustcSpeedMaxLimit)
	}
	offset, err := parseQueryInt(c, "offset", 0)
	if err != nil {
		return query, err
	}
	if offset < 0 {
		return query, errors.New("offset must be non-negative")
	}

	query = service.USTCSpeedQuery{
		Start: start, End: end, Model: model, Source: source, Status: status,
		GroupBy:    groupBy,
		AccountIDs: accountIDs, TaskID: taskID, Reasoning: reasoning, Stream: stream, Profile: profile,
		InputMin: inputMin, InputMax: inputMax, OutputMin: outputMin, OutputMax: outputMax,
		Metric: metric, Interval: interval, Aggregate: aggregate, Limit: limit, Offset: offset,
	}
	return query, nil
}

func parseUSTCSpeedAccountIDs(value string) ([]int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	if len(parts) > ustcSpeedMaxAccounts {
		return nil, fmt.Errorf("account_ids cannot contain more than %d IDs", ustcSpeedMaxAccounts)
	}
	ids := make([]int64, 0, len(parts))
	seen := make(map[int64]struct{}, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || id <= 0 {
			return nil, errors.New("account_ids must be comma-separated positive integers")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func parseOptionalPositiveInt64(value, name string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}

func parseOptionalNonNegativeInt(value, name string) (*int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return nil, fmt.Errorf("%s must be a non-negative integer", name)
	}
	return &parsed, nil
}

func parseQueryInt(c *gin.Context, name string, fallback int) (int, error) {
	value := strings.TrimSpace(c.Query(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return parsed, nil
}

func oneOf(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if value == candidate {
			return true
		}
	}
	return false
}

// parseUSTCSpeedQueryValues is a small query-string adapter used by tests and
// keeps the parser independent from a router's concrete URL setup.
func parseUSTCSpeedQueryValues(values url.Values, now time.Time) (service.USTCSpeedQuery, error) {
	ginContext := &gin.Context{Request: &http.Request{URL: &url.URL{RawQuery: values.Encode()}}}
	return parseUSTCSpeedQuery(ginContext, now)
}

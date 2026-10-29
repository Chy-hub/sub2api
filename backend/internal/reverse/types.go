// Package reverse defines the provider boundary for reverse-proxied platforms
// (Qoder, WorkBuddy). Providers adapt the project's Chat Completions requests
// to each upstream protocol and return synthetic Chat Completions responses so
// the existing OpenAI gateway can keep handling billing, logging and failover.
package reverse

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Account is the provider-facing snapshot of a reverse account.
type Account struct {
	ID          int64
	Platform    string
	Provider    string
	Region      string
	Credentials map[string]any
	Extra       map[string]any
	ProxyURL    string
}

var CredentialAliases = map[string][]string{
	"uid":             {"user_id", "userId", "qoder_account_id"},
	"user_type":       {"qoder_user_type", "userType"},
	"access_token":    {"accessToken", "token", "personal_token", "personalToken", "qoder_token", "pat", "api_key"},
	"refresh_token":   {"refreshToken"},
	"device_token":    {"deviceToken", "workbuddy_device_token"},
	"machine_token":   {"machineToken", "qoder_machine_token"},
	"machine_type":    {"machineType", "qoder_machine_type"},
	"machine_code":    {"machineCode", "qoder_machine_code"},
	"machine_id":      {"machineId", "machineID"},
	"machine_salt":    {"machineSalt"},
	"session_id":      {"sessionId"},
	"enterprise_id":   {"enterpriseId"},
	"desktop_version": {"qoder_desktop_version"},
	"user_agent":      {"workbuddy_user_agent"},
	"client_version":  {"workbuddy_client_version"},
	"cli_version":     {"workbuddy_cli_version"},
	"client_name":     {"workbuddy_client_name"},
}

// CredentialString returns a trimmed string credential.
func (a *Account) CredentialString(key string) string {
	if a == nil {
		return ""
	}
	value, ok := a.Credentials[key]
	if !ok || value == nil || strings.TrimSpace(fmt.Sprint(value)) == "" {
		for _, alias := range CredentialAliases[key] {
			if v, exists := a.Credentials[alias]; exists && v != nil && strings.TrimSpace(fmt.Sprint(v)) != "" {
				return strings.TrimSpace(fmt.Sprint(v))
			}
		}
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

// ExtraString returns a trimmed string extra.
func (a *Account) ExtraString(key string) string {
	if a == nil {
		return ""
	}
	value, ok := a.Extra[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

// Model is one upstream model entry.
type Model struct {
	ID                 string   `json:"id"`
	DisplayName        string   `json:"display_name,omitempty"`
	Description        string   `json:"description,omitempty"`
	ContextLength      int64    `json:"context_length,omitempty"`
	MaxOutputTokens    int64    `json:"max_output_tokens,omitempty"`
	Efforts            []string `json:"efforts,omitempty"`
	DefaultEffort      string   `json:"default_effort,omitempty"`
	Credits            string   `json:"credits,omitempty"`
	SupportsImages     bool     `json:"supports_images,omitempty"`
	SupportsTools      bool     `json:"supports_tools,omitempty"`
	SupportsReasoning  bool     `json:"supports_reasoning,omitempty"`
	CanDisableThinking bool     `json:"can_disable_thinking,omitempty"`
}

// CheckinResult is the outcome of a daily check-in.
type CheckinResult struct {
	Success bool           `json:"success"`
	Message string         `json:"message"`
	Reward  string         `json:"reward,omitempty"`
	Date    string         `json:"date,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// TestResult is the outcome of a connectivity test.
type TestResult struct {
	Content string
}

// LoginStart describes a pending browser login.
type LoginStart struct {
	URL       string
	ExpiresAt time.Time
	// State is provider-specific opaque data kept in memory until the poll.
	State any
}

// LoginProfile is the account profile returned by the login flow.
type LoginProfile struct {
	UID          string `json:"uid,omitempty"`
	Nickname     string `json:"nickname,omitempty"`
	Email        string `json:"email,omitempty"`
	Domain       string `json:"domain,omitempty"`
	EnterpriseID string `json:"enterprise_id,omitempty"`
	UserType     string `json:"user_type,omitempty"`
	Plan         string `json:"plan,omitempty"`
}

// LoginPollResult is one poll outcome.
type LoginPollResult struct {
	Pending      bool
	AccessToken  string
	RefreshToken string
	Profile      LoginProfile
	// Credentials carries provider-normalized credential fields to persist.
	Credentials map[string]any
}

// Provider adapts Chat Completions traffic, models, check-in and refresh.
type Provider interface {
	Forward(ctx context.Context, acc *Account, ccBody []byte, stream bool) (*http.Response, error)
	ListModels(ctx context.Context, acc *Account) ([]Model, error)
	Checkin(ctx context.Context, acc *Account) (*CheckinResult, error)
	Refresh(ctx context.Context, acc *Account) (map[string]any, error)
	Test(ctx context.Context, acc *Account, model, prompt string) (*TestResult, error)
}

// LoginProvider is implemented by providers that support browser login.
type LoginProvider interface {
	StartLogin(ctx context.Context, region string) (*LoginStart, error)
	PollLogin(ctx context.Context, state any) (*LoginPollResult, error)
}

// UpstreamError is an upstream HTTP failure that should be surfaced to the
// client with a status code instead of being treated as a transport failure.
type UpstreamError struct {
	StatusCode int
	Code       string
	Message    string
	RetryAfter time.Duration
	Kind       string
	Details    map[string]any
}

func (e *UpstreamError) Error() string {
	if e == nil {
		return ""
	}
	code := strings.TrimSpace(e.Code)
	message := strings.TrimSpace(e.Message)
	switch {
	case code != "" && message != "":
		return fmt.Sprintf("upstream %d: %s %s", e.StatusCode, code, message)
	case message != "":
		return fmt.Sprintf("upstream %d: %s", e.StatusCode, message)
	case code != "":
		return fmt.Sprintf("upstream %d: %s", e.StatusCode, code)
	default:
		return fmt.Sprintf("upstream %d", e.StatusCode)
	}
}

// NewUpstreamError builds an UpstreamError with a normalized status.
func NewUpstreamError(status int, code, message string) *UpstreamError {
	if status < 400 {
		status = http.StatusBadGateway
	}
	return &UpstreamError{StatusCode: status, Code: code, Message: message}
}

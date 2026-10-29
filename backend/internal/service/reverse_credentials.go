package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

// Normalize plugin credential exports at the persistence boundary. Aliases and
// nested token objects must not survive beside the redacted canonical fields.
func normalizeReverseCredentials(platform string, credentials map[string]any) (map[string]any, error) {
	normalized, err := normalizeReverseCredentialFields(platform, credentials)
	if err != nil {
		return nil, err
	}
	if IsReverseProvider(platform, "qoder") {
		acc := &reverse.Account{Credentials: normalized}
		token, kind := acc.CredentialString("machine_token"), acc.CredentialString("machine_type")
		if (token == "") != (kind == "") {
			return nil, fmt.Errorf("machine_token and machine_type must be configured together")
		}
	}
	return normalized, nil
}

func normalizeReverseCredentialFields(platform string, credentials map[string]any) (map[string]any, error) {
	if !IsReversePlatform(platform) {
		return credentials, nil
	}
	out := make(map[string]any, len(credentials))
	for key, value := range credentials {
		out[key] = value
	}
	for _, section := range []string{"auth", "account"} {
		if nested, ok := out[section].(map[string]any); ok {
			for key, value := range nested {
				if _, exists := out[key]; !exists {
					out[key] = value
				}
			}
			delete(out, section)
		}
	}
	provider, realm, _ := domain.ReverseProviderOf(platform)
	if vendor, ok := out["vendor"].(string); ok && vendor != "" && !strings.EqualFold(vendor, provider+realm) {
		return nil, fmt.Errorf("credential provider or region does not match the account platform")
	}
	if region, ok := out["realm"].(string); ok && region != "" && !strings.EqualFold(region, realm) {
		return nil, fmt.Errorf("credential region does not match the account platform")
	}
	if secret, ok := out["secret"].(string); ok && strings.TrimSpace(secret) != "" {
		var nested map[string]any
		if json.Unmarshal([]byte(secret), &nested) == nil {
			for key, value := range nested {
				if _, exists := out[key]; !exists {
					out[key] = value
				}
			}
		} else if token, _ := out["access_token"].(string); token == "" {
			out["access_token"] = strings.TrimSpace(secret)
		}
		delete(out, "secret")
	}
	if secret, ok := out["access_token"].(string); ok && strings.HasPrefix(strings.TrimSpace(secret), "{") {
		var tokens map[string]any
		if json.Unmarshal([]byte(secret), &tokens) == nil {
			if token, _ := tokens["device_token"].(string); strings.TrimSpace(token) != "" {
				out["access_token"] = token
				if refresh, _ := tokens["refresh_token"].(string); refresh != "" {
					out["refresh_token"] = refresh
				}
			}
		}
	}
	acc := &reverse.Account{Credentials: out}
	for canonical, aliases := range reverse.CredentialAliases {
		if value := acc.CredentialString(canonical); value != "" {
			out[canonical] = value
		}
		for _, alias := range aliases {
			delete(out, alias)
		}
	}
	if acc.CredentialString("access_token") == "" && acc.CredentialString("device_token") != "" {
		out["access_token"] = acc.CredentialString("device_token")
	}
	for key, value := range out {
		if text, ok := value.(string); ok && strings.ContainsAny(text, "\r\n") && key != "model_mapping" {
			// Only header-bearing credential fields need this restriction.
			switch key {
			case "access_token", "refresh_token", "machine_token", "machine_type", "machine_code", "machine_id", "device_token", "user_agent", "client_version", "cli_version", "client_name", "desktop_version", "uid", "domain", "enterprise_id", "session_id":
				return nil, fmt.Errorf("%s cannot contain line breaks", key)
			}
		}
	}
	return out, nil
}

var reverseTaskKinds = map[string]bool{"growth": true, "travel": true, "activity": true, "keepalive": true, "blackcat": true, "school": true, "streak": true, "dailybonus": true}

func reverseTaskHours(raw any) ([]int, error) {
	var values []any
	switch typed := raw.(type) {
	case []any:
		values = typed
	case []int:
		for _, value := range typed {
			values = append(values, value)
		}
	default:
		return nil, fmt.Errorf("task hours must be an array of integers")
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("task hours cannot be empty")
	}
	seen := map[int]bool{}
	hours := make([]int, 0, len(values))
	for _, value := range values {
		var hour int
		switch typed := value.(type) {
		case int:
			hour = typed
		case float64:
			hour = int(typed)
			if float64(hour) != typed {
				return nil, fmt.Errorf("task hours must be integers")
			}
		default:
			return nil, fmt.Errorf("task hours must be integers")
		}
		if hour < 0 || hour > 23 {
			return nil, fmt.Errorf("task hours must be between 0 and 23")
		}
		if !seen[hour] {
			seen[hour] = true
			hours = append(hours, hour)
		}
	}
	return hours, nil
}

func validateReverseExtra(platform string, extra map[string]any) error {
	if !IsReversePlatform(platform) {
		return nil
	}
	for _, key := range []string{ReverseAutoCheckinExtraKey, ReverseAutoTasksExtraKey, "reverse_auto_refresh", "reverse_sanitize_fingerprints", "reverse_passthrough_ip"} {
		if value, exists := extra[key]; exists {
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s must be a boolean", key)
			}
		}
	}
	if mode, exists := extra[ReversePromptModeExtraKey]; exists && mode != "passthrough" && mode != "custom" && mode != "append" {
		return fmt.Errorf("invalid reverse prompt mode")
	}
	if raw, exists := extra["reverse_task_kinds"]; exists {
		var kinds []string
		switch value := raw.(type) {
		case []string:
			kinds = value
		case []any:
			for _, kind := range value {
				text, ok := kind.(string)
				if !ok {
					return fmt.Errorf("task kinds must be strings")
				}
				kinds = append(kinds, text)
			}
		default:
			return fmt.Errorf("task kinds must be an array")
		}
		for _, kind := range kinds {
			if !reverseTaskKinds[kind] {
				return fmt.Errorf("unsupported task kind %q", kind)
			}
		}
	}
	if raw, exists := extra["reverse_task_hours"]; exists {
		hours, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("task hours must be an object")
		}
		for kind, value := range hours {
			if !reverseTaskKinds[kind] {
				return fmt.Errorf("unsupported task kind %q", kind)
			}
			if _, err := reverseTaskHours(value); err != nil {
				return err
			}
		}
	}
	return nil
}

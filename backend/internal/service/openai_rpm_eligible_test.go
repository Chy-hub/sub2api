package service

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestIsRPMEligible(t *testing.T) {
	tests := []struct {
		name     string
		platform string
		atype    string
		expected bool
	}{
		{"anthropic oauth", PlatformAnthropic, AccountTypeOAuth, true},
		{"anthropic setup-token", PlatformAnthropic, AccountTypeSetupToken, true},
		{"anthropic apikey (not eligible)", PlatformAnthropic, AccountTypeAPIKey, false},
		{"openai apikey", PlatformOpenAI, AccountTypeAPIKey, false},
		{"openai oauth (not eligible)", PlatformOpenAI, AccountTypeOAuth, false},
		{"gemini apikey (not eligible)", PlatformGemini, AccountTypeAPIKey, false},
		{"grok apikey (not eligible)", PlatformGrok, AccountTypeAPIKey, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Account{Platform: tt.platform, Type: tt.atype}
			require.Equal(t, tt.expected, a.IsRPMEligible())
		})
	}
}

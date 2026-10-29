package workbuddy

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

// TestCredentialParsesFlatShape verifies the project's flat credentials.
func TestCredentialParsesFlatShape(t *testing.T) {
	cred := newCredential(&reverse.Account{
		Region: "global",
		Credentials: map[string]any{
			"access_token":  "at",
			"refresh_token": "rt",
			"uid":           "u1",
			"nickname":      "nick",
			"domain":        "www.workbuddy.ai",
			"enterprise_id": "ent",
			"device_token":  "dt",
			"machine_id":    "m1",
			"session_id":    "s1",
			"machine_salt":  "salt",
		},
	})
	if cred.accessToken != "at" || cred.refreshToken != "rt" || cred.uid != "u1" || cred.nickname != "nick" {
		t.Fatalf("flat credential parse failed: %+v", cred)
	}
	if cred.domain != "www.workbuddy.ai" || cred.enterpriseID != "ent" || cred.deviceToken != "dt" {
		t.Fatalf("flat metadata parse failed: %+v", cred)
	}
	if cred.region != regionGlobal {
		t.Fatalf("region = %q", cred.region)
	}
	if cred.machineID() != "m1" || cred.sessionID() != "s1" {
		t.Fatal("explicit machine/session ids must win")
	}
}

// TestCredentialParsesNestedShape verifies the reference plugin's nested
// auth.* / account.* shape.
func TestCredentialParsesNestedShape(t *testing.T) {
	cred := newCredential(&reverse.Account{
		Credentials: map[string]any{
			"auth": map[string]any{
				"accessToken":  "at",
				"refreshToken": "rt",
				"domain":       "www.workbuddy.ai",
				"realm":        "global",
			},
			"account": map[string]any{
				"uid":          "u1",
				"enterpriseId": "ent",
				"nickname":     "nick",
			},
			"device_token": "dt",
		},
	})
	for key, got := range map[string]string{
		"access":   cred.accessToken,
		"refresh":  cred.refreshToken,
		"uid":      cred.uid,
		"nickname": cred.nickname,
		"domain":   cred.domain,
		"ent":      cred.enterpriseID,
		"device":   cred.deviceToken,
	} {
		if got == "" {
			t.Fatalf("nested credential field %s is empty", key)
		}
	}
	if cred.region != regionGlobal {
		t.Fatalf("region = %q, want global", cred.region)
	}

	// Flat keys win over nested values when both are present.
	cred = newCredential(&reverse.Account{
		Region: "cn",
		Credentials: map[string]any{
			"uid":     "flat",
			"account": map[string]any{"uid": "nested"},
		},
	})
	if cred.uid != "flat" {
		t.Fatalf("uid = %q, want flat", cred.uid)
	}
}

// TestCredentialRegionInference verifies the domain fallback when the account
// has no explicit region.
func TestCredentialRegionInference(t *testing.T) {
	if got := newCredential(&reverse.Account{Credentials: map[string]any{"domain": "https://www.workbuddy.ai/"}}).region; got != regionGlobal {
		t.Fatalf("global domain → %q", got)
	}
	if got := newCredential(&reverse.Account{Credentials: map[string]any{"domain": "www.codebuddy.cn"}}).region; got != regionCN {
		t.Fatalf("cn domain → %q", got)
	}
}

// TestDerivedIDStability verifies the documented derivation:
// sha256("wb2a:"+purpose+":"+uid+":"+salt)[:18] hex, stable per input and
// isolated by purpose/salt.
func TestDerivedIDStability(t *testing.T) {
	first := deriveID("u1", "machine", "salt")
	second := deriveID("u1", "machine", "salt")
	if first != second {
		t.Fatalf("derivation is not stable: %q vs %q", first, second)
	}
	if len(first) != 36 {
		t.Fatalf("derived id length = %d, want 36", len(first))
	}
	for _, char := range first {
		if !strings.ContainsRune("0123456789abcdef", char) {
			t.Fatalf("derived id %q is not hex", first)
		}
	}
	if deriveID("u1", "machine", "other-salt") == first {
		t.Fatal("salt must isolate the ID space")
	}
	if deriveID("u1", "session", "salt") == first {
		t.Fatal("purpose must isolate IDs")
	}
	if deriveID("u2", "machine", "salt") == first {
		t.Fatal("uid must isolate IDs")
	}
	if deriveID("", "machine", "salt") != "" {
		t.Fatal("empty uid must derive no ID")
	}
}

// TestBuildCacheKeyIsolation verifies the per-account prompt cache prefix.
func TestBuildCacheKeyIsolation(t *testing.T) {
	one := buildCacheKey("1234567890", "conversation")
	if !strings.HasPrefix(one, "wb2a-12345678-") {
		t.Fatalf("cache key = %q", one)
	}
	if one != buildCacheKey("1234567890", "conversation") {
		t.Fatal("cache key must be stable")
	}
	if one == buildCacheKey("1234567890", "other conversation") {
		t.Fatal("conversation must affect the cache key")
	}
	if one == buildCacheKey("abcdefghij", "conversation") {
		t.Fatal("uid prefix must isolate accounts")
	}
}

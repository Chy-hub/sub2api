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

// TestCredentialDefaultsFollowTheRegion locks that the version defaults are
// resolved per deployment. The two genuine clients differ (CN 5.8.3 / CLI
// 2.179.0, Global 5.8.4 / 2.161.0), and the region is only known after the
// domain is inspected — so a default captured before that would be wrong for
// every account that infers its region.
func TestCredentialDefaultsFollowTheRegion(t *testing.T) {
	global := newCredential(&reverse.Account{Credentials: map[string]any{"domain": "https://www.workbuddy.ai/"}})
	if global.clientVersion != "5.8.4" || global.cliVersion != "2.161.0" {
		t.Fatalf("global defaults = %s/%s", global.clientVersion, global.cliVersion)
	}
	cn := newCredential(&reverse.Account{Region: "cn", Credentials: map[string]any{}})
	if cn.clientVersion != "5.8.3" || cn.cliVersion != "2.179.0" {
		t.Fatalf("cn defaults = %s/%s", cn.clientVersion, cn.cliVersion)
	}
	// An explicit credential value still wins over the region default.
	override := newCredential(&reverse.Account{Region: "global", Credentials: map[string]any{"client_version": "9.9.9"}})
	if override.clientVersion != "9.9.9" {
		t.Fatalf("client_version override = %q", override.clientVersion)
	}
}

// TestNewUUIDv7IsCanonical locks the shape the upstream channel check expects.
func TestNewUUIDv7IsCanonical(t *testing.T) {
	seen := make(map[string]struct{}, 64)
	for i := 0; i < 64; i++ {
		id := newUUIDv7()
		if !isCanonicalUUID(id) {
			t.Fatalf("%q is not a canonical UUID", id)
		}
		// Version 7 and RFC 9562 variant bits.
		if id[14] != '7' {
			t.Fatalf("%q is not version 7", id)
		}
		if !strings.ContainsRune("89ab", rune(id[19])) {
			t.Fatalf("%q has the wrong variant nibble", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("%q repeated", id)
		}
		seen[id] = struct{}{}
	}
}

// TestIsCanonicalUUIDRejectsMalformed locks that shapes the upstream would
// reject are replaced rather than forwarded.
func TestIsCanonicalUUIDRejectsMalformed(t *testing.T) {
	valid := "01a1ca0e-2bf7-7a2c-9120-44a92a757e40"
	if !isCanonicalUUID(valid) {
		t.Fatal("valid UUID rejected")
	}
	for _, bad := range []string{
		"",
		"conv-1",
		"01a1ca0e2bf77a2c912044a92a757e40",        // no hyphens
		"01a1ca0e-2bf7-7a2c-9120-44a92a757e4",     // too short
		"01a1ca0e-2bf7-7a2c-9120-44a92a757e400",   // too long
		"01a1ca0e-2bf7-7a2c-9120-44a92a757e4g",    // non-hex
		"01a1ca0e_2bf7_7a2c_9120_44a92a757e40",    // wrong separator
		"01a1ca0e-2bf7-7a2c-9120-44a92a7574-0e40", // hyphen in the wrong place
	} {
		if isCanonicalUUID(bad) {
			t.Fatalf("%q must not be accepted as a canonical UUID", bad)
		}
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

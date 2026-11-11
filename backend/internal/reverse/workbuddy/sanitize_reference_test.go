package workbuddy

import (
	"strings"
	"testing"
)

// Regression cases ported from CLIProxyAPI-freetier2api-plugin (MIT).
func TestSanitizeRemovesFingerprints(t *testing.T) {
	rules, regexes := loadSanitizeRules()
	if len(rules.Rewrites) == 0 && len(regexes) == 0 {
		t.Skip("sanitize rules are not embedded")
	}
	_ = regexes

	// 改写表里的任一条命中都应产生变化。
	if len(rules.Rewrites) > 0 {
		pair := rules.Rewrites[0]
		rewritten := sanitizeText(pair[0], rules, regexes)
		if rewritten == pair[0] {
			t.Fatalf("rewrite rule did not apply: %q", pair[0])
		}
	}
	// 不命中特征串的文本原样返回（快路径）。
	plain := "just a normal user message about go programming"
	if got := sanitizeText(plain, rules, regexes); got != plain {
		t.Fatalf("plain text must pass through unchanged, got %q", got)
	}
}

func TestSanitizeMessagesCoversToolArguments(t *testing.T) {
	rules, _ := loadSanitizeRules()
	if len(rules.Rewrites) == 0 {
		t.Skip("sanitize rules are not embedded")
	}
	needle := rules.Rewrites[0][0]

	messages := []any{map[string]any{
		// content 为 null（工具调用消息的常见形态）。
		"role":    "assistant",
		"content": nil,
		"tool_calls": []any{map[string]any{
			"id": "c1",
			"function": map[string]any{
				"name":      "Write",
				"arguments": `{"content":"` + needle + `"}`,
			},
		}},
	}}
	sanitizeMessages(messages)

	call := messages[0].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	arguments := call["function"].(map[string]any)["arguments"].(string)
	if strings.Contains(arguments, needle) {
		t.Fatal("tool_call arguments must be sanitized even when content is null")
	}
}

func TestSanitizeRewritesDeepSeekHarnessFingerprint(t *testing.T) {
	rules, regexes := loadSanitizeRules()
	// The DSH kernel sentence triggers upstream 11128 when passed verbatim.
	const text = "You are an AI agent powered by DeepSeek Harness."
	got := sanitizeText(text, rules, regexes)
	if got == text {
		t.Fatal("DeepSeek Harness fingerprint must be rewritten")
	}
	if strings.Contains(got, "DeepSeek Harness") {
		t.Fatalf("rewritten text still carries the literal fingerprint: %q", got)
	}
}

func TestSanitizeStripsZeroWidthCharactersInsideFingerprint(t *testing.T) {
	rules, regexes := loadSanitizeRules()
	// Some DSH builds insert U+200B between the words, which defeats a plain
	// string replace on the literal.
	const text = "You are an AI agent powered by DeepSeek​ Harness."
	got := sanitizeText(text, rules, regexes)
	if strings.Contains(got, "​") {
		t.Fatalf("zero-width characters must be stripped once a fingerprint matches: %q", got)
	}
	if strings.Contains(got, "DeepSeek Harness") {
		t.Fatalf("rewritten text still carries the literal fingerprint: %q", got)
	}
}

func TestSanitizeKeepsLineStructure(t *testing.T) {
	rules, regexes := loadSanitizeRules()
	// Stripping regexes end in \s*, so an over-broad pattern would swallow the
	// newline and collapse the system prompt into a single line.
	const text = "You are Claude Code\nx-anthropic-billing-header: abc;\nsecond line\nthird line"
	got := sanitizeText(text, rules, regexes)
	if strings.Count(got, "\n") != strings.Count(text, "\n") {
		t.Fatalf("sanitize must not change the line count: %q", got)
	}
}

// TestSanitizeRewritesWorkspaceRootFingerprint locks the second half of the
// 11128 fix. Replacing the brand was not enough: a DeepSeek-Harness system
// prompt still carries its @-path workspace convention
// ("Tokens prefixed with @ are paths ... resolve from the workspace root"), and
// the upstream rejects on that structure on its own.
//
// The line keeps its readability: only "workspace root" collapses to
// "workspace", so the instruction still parses as an instruction.
func TestSanitizeRewritesWorkspaceRootFingerprint(t *testing.T) {
	rules, regexes := loadSanitizeRules()
	const text = "Tokens prefixed with @ are paths.\nResolve them from the workspace root before writing."
	got := sanitizeText(text, rules, regexes)
	if strings.Contains(got, "workspace root") {
		t.Fatalf("workspace root must be rewritten: %q", got)
	}
	if !strings.Contains(got, "workspace") {
		t.Fatalf("the instruction must survive the rewrite: %q", got)
	}
	if strings.Contains(got, "Tokens prefixed with @") {
		t.Fatalf("the @-path signature must be rewritten too: %q", got)
	}
}

// TestSanitizeIsIdempotent locks that running the rules twice changes nothing
// more: the rewrite targets are themselves fingerprints, so an accidental
// self-match would keep mutating the text.
func TestSanitizeIsIdempotent(t *testing.T) {
	rules, regexes := loadSanitizeRules()
	const text = "You are an AI agent powered by DeepSeek Harness.\nTokens prefixed with @ are paths resolved from the workspace root."
	once := sanitizeText(text, rules, regexes)
	twice := sanitizeText(once, rules, regexes)
	if once != twice {
		t.Fatalf("sanitize is not idempotent:\n once: %q\ntwice: %q", once, twice)
	}
}

func TestSanitizeLeavesPlainTextUntouched(t *testing.T) {
	rules, regexes := loadSanitizeRules()
	// No fingerprint hit means no character-level rewrite at all, even when the
	// text carries zero-width characters.
	const text = "ordinary user message with a zero width​ space"
	if got := sanitizeText(text, rules, regexes); got != text {
		t.Fatalf("plain text must pass through unchanged: %q", got)
	}
}

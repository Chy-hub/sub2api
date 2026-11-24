package qoder

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/reverse/textsanitize"
)

// 声明句用中性产品名做输入，避免在源码里写死品牌字面量；真实 DSH 句子的覆盖
// 由 TestNeutralizeCoversSharedRewriteTarget 从规则表取值来锁。

func TestNeutralizeDeclaration(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "keeps trailing instruction",
			in:   "You are an AI agent powered by FooCorp.\nFollow the rules below.",
			want: neutralIdentity + "\nFollow the rules below.",
		},
		{
			name: "official cli wording",
			in:   "You are BarCode, an official CLI tool for Bar.",
			want: neutralIdentity,
		},
		{
			name: "coding agent wording",
			in:   "You are a coding agent running in the Baz CLI tool, a terminal-based coding assistant.",
			want: neutralIdentity,
		},
		{
			name: "markdown heading prefix",
			in:   "## You are an AI agent powered by FooCorp.",
			want: "## " + neutralIdentity,
		},
		{
			name: "no terminating period",
			in:   "You are omp, a coding agent",
			want: neutralIdentity,
		},
		{
			name: "already neutral",
			in:   neutralIdentity,
			want: neutralIdentity,
		},
		{
			name: "unrelated text",
			in:   "Summarize the document in three bullets.",
			want: "Summarize the document in three bullets.",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := neutralizeDeclaration(test.in); got != test.want {
				t.Fatalf("neutralizeDeclaration(%q) = %q, want %q", test.in, got, test.want)
			}
			// 幂等：替换结果再跑一遍不应继续变化。
			if again := neutralizeDeclaration(test.want); again != test.want {
				t.Fatalf("not idempotent: %q -> %q", test.want, again)
			}
		})
	}
}

// TestNeutralizeCoversSharedRewriteTarget 取通用规则表里的实际改写结果作为输入：
// 那正是 Qoder 目前会收到的文本（品牌被换成 WorkBuddy），它必须再被换成通用身份。
func TestNeutralizeCoversSharedRewriteTarget(t *testing.T) {
	rules, _ := textsanitize.Load()
	if len(rules.Rewrites) <= 6 {
		t.Skip("sanitize rules are not embedded")
	}
	declared := rules.Rewrites[6][1]
	if got := neutralizeDeclaration(declared); got != neutralIdentity {
		t.Fatalf("shared rewrite target %q must collapse to a neutral identity, got %q", declared, got)
	}
}

func TestNeutralizeAgentDeclarationsScopesToSystemRoles(t *testing.T) {
	messages := []any{
		map[string]any{"role": "system", "content": "You are an AI agent powered by FooCorp.\nBe brief."},
		map[string]any{"role": "developer", "content": "You are BarCode, an official CLI tool for Bar."},
		// 用户消息里出现同样的句子是用户自己的内容，不该被改写。
		map[string]any{"role": "user", "content": "You are an AI agent powered by FooCorp."},
		// parts 形式的内容也要覆盖。
		map[string]any{"role": "system", "content": []any{
			map[string]any{"type": "text", "text": "You are a coding agent running in the Baz CLI tool."},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AA"}},
		}},
	}
	if !neutralizeAgentDeclarations(messages) {
		t.Fatal("expected a change")
	}
	if got := messages[0].(map[string]any)["content"]; got != neutralIdentity+"\nBe brief." {
		t.Fatalf("system message = %q", got)
	}
	if got := messages[1].(map[string]any)["content"]; got != neutralIdentity {
		t.Fatalf("developer message = %q", got)
	}
	if got := messages[2].(map[string]any)["content"]; got != "You are an AI agent powered by FooCorp." {
		t.Fatalf("user message must stay untouched, got %q", got)
	}
	parts := messages[3].(map[string]any)["content"].([]any)
	if got := parts[0].(map[string]any)["text"]; got != neutralIdentity {
		t.Fatalf("system part = %q", got)
	}
	if image, ok := parts[1].(map[string]any)["image_url"]; !ok || image == nil {
		t.Fatal("image part must be preserved")
	}
}

// TestForwardNeutralizesSystemDeclaration locks the wiring and the switch: the
// declaration must not survive into the envelope, and the switch must still be
// able to pass it through untouched.
func TestForwardNeutralizesSystemDeclaration(t *testing.T) {
	rules, _ := textsanitize.Load()
	if len(rules.Rewrites) <= 6 {
		t.Skip("sanitize rules are not embedded")
	}
	declared := rules.Rewrites[6][1]

	for _, test := range []struct {
		name      string
		extra     map[string]any
		wantPlain bool
	}{
		{"default on", map[string]any{}, false},
		{"explicit off", map[string]any{"reverse_sanitize_fingerprints": false}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, envelopes, _ := fakeChatUpstream(t)
			defer server.Close()
			installFakeEndpoints(t, server)

			provider := New().(*Provider)
			account := newTestAccount(600, test.extra, nil)
			body, errMarshal := json.Marshal(map[string]any{
				"model": "dmodel",
				"messages": []any{
					map[string]any{"role": "system", "content": declared},
					map[string]any{"role": "user", "content": "hi"},
				},
			})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			forwardOnce(t, provider, account, string(body))
			envelope := <-envelopes

			messages, _ := envelope["messages"].([]any)
			if len(messages) < 2 {
				t.Fatalf("forwarded messages = %#v", messages)
			}
			systemText := rawStringValue(messages[0].(map[string]any)["content"])
			if test.wantPlain {
				if systemText != declared {
					t.Fatalf("sanitization off must pass the declaration through, got %q", systemText)
				}
				return
			}
			if systemText != neutralIdentity {
				t.Fatalf("declaration must be neutralized, got %q", systemText)
			}
			if strings.Contains(strings.ToLower(systemText), "you are") && !strings.Contains(systemText, neutralIdentity) {
				t.Fatalf("unexpected identity left behind: %q", systemText)
			}
		})
	}
}

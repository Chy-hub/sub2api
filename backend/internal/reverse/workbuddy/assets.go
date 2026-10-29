package workbuddy

import (
	_ "embed"
	"strings"
)

// Embedded fallbacks (see assets/NOTICE.md for provenance).
//
//go:embed assets/prompt_default.md
var defaultPromptRaw string

//go:embed assets/catalog_seed.json
var catalogSeedRaw []byte

// defaultPromptText is the fallback system prompt used when an account selects
// reverse_prompt_mode=custom|append without providing reverse_prompt_text.
func defaultPromptText() string {
	return strings.TrimSpace(defaultPromptRaw)
}

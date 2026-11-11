# Source attribution

The WorkBuddy protocol, request normalization, fingerprint sanitization rules,
account task workflows, desktop/web/miniprogram event sequences and reference
regression tests are adapted from CLIProxyAPI-freetier2api-plugin
(https://github.com/cxy2730/CLIProxyAPI-freetier2api-plugin, `internal/vendors/workbuddy`,
source revision `e90609d`). The upstream repository was formerly published under
the `jiandanc` account, which is gone; the current one carries the full history.

`prompt_default.md`, `catalog_seed.json` and `sanitize_rules.json` are copied
from its embedded assets. Its copyright and MIT license are reproduced in
`../../LICENSE.MIT`.

The original project credits Sliverkiss/workbuddy2api and
linguo2625469/workbuddy2api-panel (MIT) for protocol, prompt filtering and
account task references. This port uses sub2api account storage, account proxies,
gateway protocol conversion, token accounting, scheduling and admin UI in place
of the plugin's host ABI, credential files and standalone console.

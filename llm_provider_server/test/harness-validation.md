# Web Harness validation

This record covers the Codex and Claude Code process adapters and Gateway HTTP integration. A skipped or credential-blocked live check is not a pass.

The 2026-09-17 user-approved per-Run Claude transport session and exact
continuation contract supersede the old stateless-Claude requirement. This is not
permission to restore one cold Claude invocation per turn and does not give the
Gateway Agent-loop, memory, or tool-execution ownership.

## Pinned artifacts

### Codex

- Package: `@openai/codex@0.162.1`
- CLI output: `codex-cli 0.162.1`
- npm integrity: `sha512-NWZdi/kxyjv/8EUGFupziGU38YyleugZRM4JXgY5XFH7FUmaFA33NZS2Bmq0HPazf7S3jJQQWsZ/jAK9jsrV3Q==`
- Tarball: `https://registry.npmjs.org/@openai/codex/-/codex-0.162.1.tgz`
- Install: `npm install --global @openai/codex@0.162.1`
- Verify:

```bash
npm view '@openai/codex@0.162.1' version dist.integrity dist.tarball
codex --version
codex login status
```

The Gateway drives it as `codex app-server` (JSON-RPC over stdio). This version
provides the two protocol features the adapter depends on: `thread/start.dynamicTools`
(behind `initialize.capabilities.experimentalApi`) and `thread/inject_items`.

### Claude Code

- Wrapper package: `@anthropic-ai/claude-code@2.1.269`
- Wrapper integrity: `sha512-osSbRU1KjlAfhVSgso7g+KxCr5DLNlfm7xeRPpm/c9s+7HGqQLHbkUYvjepbe6TiHJa9iSeirW/t7vW4sZhTIQ==`
- Windows native package: `@anthropic-ai/claude-code-win32-x64@2.1.269`
- Windows native integrity: `sha512-kVW7UFGm7XqU7QvyksscCyiMOFDJhHSJX3M4YWBP9cde+Aj48K2LHnOvyM4aCO5mIPQl/j7tUVClUMQwsOf1tQ==`
- CLI output: `2.1.269 (Claude Code)`
- Install: `npm install --global @anthropic-ai/claude-code@2.1.269`
- Verify:

```powershell
npm view '@anthropic-ai/claude-code@2.1.269' version dist.integrity dist.tarball
npm view '@anthropic-ai/claude-code-win32-x64@2.1.269' version dist.integrity dist.tarball
claude --version
claude auth status
```

The temporary validation install used this native executable:

```text
C:\Users\admin\AppData\Local\Temp\opencode\claude-code-2.1.269\node_modules\@anthropic-ai\claude-code-win32-x64\claude.exe
```

## Production process boundary

Both adapters require an absolute executable path and an explicit credential or
configured local-login snapshot.

Codex runs one long-lived `codex app-server` per credential (or per local-login
snapshot) in its own private `CODEX_HOME`, removed when the process exits. It is
started lazily, replaced when it dies, stopped after 30 minutes idle, and killed
with its process tree on Gateway shutdown. The API key is passed with
`account/login/start`, never through the environment; a copied local login must
pass `account/read`. Every Generate runs on a fresh `ephemeral` thread with
`sandbox: read-only` and `approvalPolicy: never`:

1. `thread/start` with the leading system messages as `baseInstructions` and the
   domain tools as native `dynamicTools` under wire-safe names;
2. `thread/inject_items` with the earlier history as Responses API items
   (`message`, `function_call`, `function_call_output`);
3. `turn/start` with the trailing user messages, or empty input when the request
   ends with tool results.

The first `item/tool/call` interrupts the turn and is returned as
`finish_reason: "tool_call"`; the Agent runtime runs the tool and sends a new
Generate. The Gateway therefore keeps no Codex conversation state, and a retried
Generate is safe. Codex's internal web-search steps stay inside one Generate;
Usage is the fresh thread's total.

Stateless Claude creates a new private working/config directory per call. Managed
Claude creates an isolated directory per conversation/signature and removes it on
replacement, TTL, or shutdown. Each process:

- replaces `HOME`, `USERPROFILE`, `CODEX_HOME`, and `CLAUDE_CONFIG_DIR`;
- passes only the selected provider credential and the small OS/TLS/proxy environment needed by the native executable;
- verifies the exact pinned CLI version;
- uses Codex ephemeral threads, stateless Claude no-session-persistence, or managed Claude's bounded `--session-id`/`--resume` transport mode;
- retains at most 1 MiB of Claude stdout and 64 KiB of stderr while continuing to drain both streams; a Codex JSON-RPC line over 1 MiB fails the request and restarts the app-server;
- maps recognized model/quota/login failures to fixed public categories and never returns arbitrary stdout/stderr prose;
- terminates the invocation process tree on cancellation and when the direct process exits.

Windows uses a per-invocation Job Object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`. Supported Unix targets use a separate process group. Other targets kill the direct process and are not claimed to have descendant containment.

For Claude, domain tools are serialized into the prompt as return-only schemas and are not registered with the CLI. For Codex they are native dynamic tools that Codex can only ask the Gateway to run. Unknown returned tool names remain normalized intentionally so the Agent runtime can apply its approved budgeted unknown-tool error path.

Codex 0.162 routes dynamic tools through its code-mode host, so `code_mode` and
`code_mode_host` must stay enabled; disabling them makes every dynamic tool call
fail closed. The other capability features stay disabled with `--disable`.

## Offline reproducible tests

Run only the dedicated harness tests:

```powershell
Set-Location llm_provider_server
go test ./test -run '^(TestCodexAppServer|TestClaudeCodeHarness)' -count=1
```

Repeated validation performed on Windows with Go `1.24.4`:

```text
go test ./test -run '^(TestCodexHarness|TestClaudeCodeHarness)' -count=10
ok stock-ai/llm-provider-server/test 77.459s
```

Compile production packages without running unrelated tests:

```text
go test . ./cmd/... -run '^$' -count=1
? stock-ai/llm-provider-server [no test files]
? stock-ai/llm-provider-server/cmd/llm-provider-server [no test files]
```

The fixtures verify complete ordered arguments and the isolated environment
allowlist, the Claude output schema, the Codex JSON-RPC thread/turn contract
(ephemeral read-only thread, `dynamicTools`, injected history, first-call
interrupt), structured text/tool-call and Usage normalization, API-key and
copied-login authentication boundaries, one app-server per credential, version
mismatch, safe authentication/quota diagnostics, malformed output, HTTP error
mapping, timeout/cancellation without losing the app-server, oversized output,
descendant cleanup, and signal-driven Gateway cleanup of temporary login
material. The Codex oversized-output fixture emits a 2 MiB line; the adapter fails
the request without waiting for timeout and returns `ErrWebHarnessResponse`.

The continuation regression fixture compares an identical full-history workload:

```text
legacy resumed suffix:        287 exact bytes, 2 messages, 1 assistant message
explicit continuation delta:  177 exact bytes, 1 message,  0 assistant messages
cold invocations added:       0
full cold fallback/baseline:  764 exact bytes / 764 exact bytes
```

It also verifies one full cold invocation for wrong acknowledged counts, changed
prior assistant output, changed or empty deltas, and missing retained state. These
are deterministic payload facts, not live token, billing, latency, or cache claims.

Work-B verification on Windows with offline fixtures only (2026-09-17):

```text
go test ./... -count=1
ok stock-ai/llm-provider-server/test 60.279s

go test ./test -run '^(TestCodexHarnessPinsCapabilitiesAndNormalizesStructuredToolReturn|TestNativeAndCodexIgnoreContinuationAndUseFullMessages|TestClaudeContinuationUsesExactDeltaWithoutExtraColdOrAssistantDuplication|TestClaudeInvalidOrUnprovableContinuationFallsBackToOneFullColdInvocation|TestClaudeConversationFailedResumeWithConcurrentWaiterNeverInitializesUUIDTwice)$' -count=10
ok stock-ai/llm-provider-server/test 107.543s

go vet ./...
(no findings)
```

## Codex 0.162.1 live observations

Date: 2026-10-17, WSL2 Ubuntu, standalone `codex-cli 0.162.1`, local ChatGPT
(Plus) login copied through `LLM_SERVER_CODEX_AUTH_FILE`.

Protocol spike (JSON-RPC directly against `codex app-server` with the production
flags):

- `initialize` with `experimentalApi`, `account/read`, `thread/start`
  (`ephemeral`, `read-only`, `never`, `dynamicTools`), `thread/inject_items`,
  `turn/start`, `turn/interrupt` and `thread/unsubscribe` all succeeded; requests
  without a `jsonrpc` member are accepted.
- With `code_mode`/`code_mode_host` disabled, the model never received the
  dynamic tool ("Code Mode ... will fail closed"). With them enabled,
  `item/tool/call` arrived with call ID `exec-<uuid>` and the requested arguments,
  and `turn/interrupt` ended the turn as `interrupted`.
- Injected `message`/`function_call`/`function_call_output` history followed by an
  empty-input `turn/start` produced the final answer from the injected tool result.
- An explicit `gpt-5.4-mini` model returned HTTP 400 "not supported when using
  Codex with a ChatGPT account"; the ChatGPT login needs `cli-default`.

End-to-end through the built Gateway binary (`local_login`, `cli-default`,
`reasoning_effort: low`, no `PATH` in the app-server environment):

```text
step 1 /v1/generate -> finish_reason tool_call, tool market.price {"symbol":"BTCUSDT"},
                       usage input 7892 / output 33 / total 7925
step 2 /v1/generate (with the tool result) -> finish_reason stop,
                       "BTCUSDT is trading at 65,000.50 USDT."
SIGINT -> Gateway exit 0, no stock-ai-codex-app-server-* directory left
```

Still unverified live: the `api_key` auth mode with a real `CODEX_API_KEY`-class
key, explicit model entitlement, and actual web-search results.

## Claude Code 2.1.269 live observations

Date: 2026-09-13. `claude auth status` reported `loggedIn: false`, `authMethod: none`.

Capability inventory was inspected with the production restrictions and `--output-format stream-json --verbose` so the initialization event was visible:

```powershell
$claude = 'C:\Users\admin\AppData\Local\Temp\opencode\claude-code-2.1.269\node_modules\@anthropic-ai\claude-code-win32-x64\claude.exe'
& $claude --restricted --bare --setting-sources '' --strict-mcp-config `
  --tools 'WebSearch,WebFetch' --allowedTools 'WebSearch,WebFetch' --disallowedTools 'mcp__*' `
  --permission-mode dontAsk --permission-prompts none --no-session-persistence `
  --disable-slash-commands --no-chrome --model claude-sonnet-4-6 -p `
  --output-format stream-json --verbose 'Search and fetch an Anthropic documentation page.'
```

Observed initialization without a JSON schema had `tools: []`, `mcp_servers: []`, `skills: []`, and `plugins: []`. Adding the production JSON schema yielded only `tools: ["StructuredOutput"]`; neither `WebSearch` nor `WebFetch` appeared. The command then returned `Failed to authenticate: OAuth session expired and could not be refreshed`, `is_error: true`, and exit code 1. The exact JSON-mode command is therefore correctly classifiable as `ErrWebHarnessAuthentication`, but native Web behavior and domain structured return could not be exercised.

Claude's documentation states that native Windows does not provide Claude Code sandboxing. The adapter therefore relies on the exact built-in tool allowlist for model capabilities and uses the Windows Job Object only for process lifecycle containment; the Job Object is not a filesystem sandbox.

Hard blocker for ticket 13: valid non-interactive authentication is unavailable, and the unauthenticated initialization did not expose the requested native Web tools. Re-run with a credential supplied through the controlled Provider-token path, then require initialization containing `WebSearch`, `WebFetch`, and `StructuredOutput` and no other executable/file/MCP tools before accepting the ticket.

## Gateway-owner integration handoff

No shared Gateway file was changed. The later single-writer integration needs to:

1. Source each executable path from trusted server configuration, never request options or model output. Codex is the `codex` entry of the Gateway's Api registry (`webharness.CodexAppServer`); Claude uses `ClaudeCodeWebHarness{Executable: absolutePath}`.
2. Convert validated Gateway messages/tools/model directly into the adapter request; register domain tools only as Codex dynamic tools, never as executable CLI tools.
3. Pass the decrypted selected Provider credential only to `Generate`, apply the configured Provider timeout to its context, and never persist the credential in Provider config.
4. Convert `WebHarnessResponse` directly to the existing normalized response. Preserve unknown tool names for the Agent runtime's budgeted error handling.
5. Map `context.DeadlineExceeded` to `PROVIDER_TIMEOUT`, `ErrWebHarnessAuthentication` to `AUTHENTICATION_FAILED`, `ErrWebHarnessResponse` to `PROVIDER_RESPONSE_INVALID`, and unavailable/version/process failures to `PROVIDER_UNAVAILABLE`; retain the existing cancellation policy.
6. Reject unsupported harness request options rather than silently ignoring them. Keep the existing model/default-model resolution.
7. Add HTTP integration tests under the Gateway owner's shared test scope; do not duplicate the process fixtures.

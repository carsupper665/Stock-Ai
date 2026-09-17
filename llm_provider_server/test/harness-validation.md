# Web Harness validation

This record covers the Codex and Claude Code process adapters and Gateway HTTP integration. A skipped or credential-blocked live check is not a pass.

The 2026-09-17 user-approved per-Run Claude transport session and exact
continuation contract supersede the old stateless-Claude requirement. This is not
permission to restore one cold Claude invocation per turn and does not give the
Gateway Agent-loop, memory, or tool-execution ownership.

## Pinned artifacts

### Codex

- Package: `@openai/codex@0.145.0`
- CLI output: `codex-cli 0.145.0`
- npm integrity: `sha512-/PSPSFujjjmiyVFvG2yu/grOFhsWdokTH8t2KGWhXSo/M5n/dIDsnbsnO82/7bLtIoDuzQf7ATBUMWqPWQINlQ==`
- Tarball: `https://registry.npmjs.org/@openai/codex/-/codex-0.145.0.tgz`
- Install: `npm install --global @openai/codex@0.145.0`
- Verify:

```powershell
npm view '@openai/codex@0.145.0' version dist.integrity dist.tarball
codex --version
codex login status
```

The validated Windows executable was:

```text
C:\Users\admin\AppData\Roaming\npm\node_modules\@openai\codex\node_modules\@openai\codex-win32-x64\vendor\x86_64-pc-windows-msvc\bin\codex.exe
```

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
configured local-login snapshot. Every Codex/stateless-Claude call creates a new
private working/config directory. Managed Claude creates an isolated directory per
conversation/signature and removes it on replacement, TTL, or shutdown. Each
process invocation:

- replaces `HOME`, `USERPROFILE`, `CODEX_HOME`, and `CLAUDE_CONFIG_DIR`;
- passes only the selected provider credential and the small OS/TLS/proxy environment needed by the native executable;
- verifies the exact pinned CLI version;
- uses Codex ephemeral mode, stateless Claude no-session-persistence, or managed Claude's bounded `--session-id`/`--resume` transport mode;
- retains at most 1 MiB stdout and 64 KiB stderr while continuing to drain both streams;
- maps recognized model/quota/login failures to fixed public categories and never returns arbitrary stdout/stderr prose;
- terminates the invocation process tree on cancellation and when the direct process exits.

Windows uses a per-invocation Job Object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`. Supported Unix targets use a separate process group. Other targets kill the direct process and are not claimed to have descendant containment.

Domain tools are serialized into the prompt as return-only schemas. They are not registered with either CLI. Unknown returned tool names remain normalized intentionally so the Agent runtime can apply its approved budgeted unknown-tool error path.

## Offline reproducible tests

Run only the dedicated harness tests:

```powershell
Set-Location llm_provider_server
go test ./test -run '^(TestCodexHarness|TestClaudeCodeHarness)' -count=1
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
allowlist, the exact output schema, structured text/tool-call and Usage
normalization, API-key and copied-login authentication boundaries, version
mismatch, safe authentication/quota diagnostics, malformed output, HTTP error
mapping, timeout/cancellation, oversized output, descendant cleanup, and
signal-driven Gateway cleanup of temporary login material. The oversized-output
fixture emits 2 MiB; the adapter retains the bounded prefix, drains the remainder,
exits without waiting for timeout, and returns `ErrWebHarnessResponse`.

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

## Codex 0.145.0 live observations

Date: 2026-09-13. Authentication available locally: `Logged in using ChatGPT`.

The capability command used the production arguments below. The local ChatGPT login required omitting `-m`; explicitly passing `gpt-5.4`, `gpt-5.3-codex`, or `gpt-5.2-codex` returned HTTP 400 “model is not supported when using Codex with a ChatGPT account”. Production does pass the requested model and uses isolated `CODEX_API_KEY`, so this local run validates the pinned binary's capability controls but is not an end-to-end `CodexWebHarness.Generate` acceptance.

```powershell
$codex = 'C:\Users\admin\AppData\Roaming\npm\node_modules\@openai\codex\node_modules\@openai\codex-win32-x64\vendor\x86_64-pc-windows-msvc\bin\codex.exe'
$schema = (Resolve-Path 'llm_provider_server\test\codex_live_response_schema.json')
$disabled = @(
  'shell_tool','apps','multi_agent','goals','hooks','memories','remote_plugin','plugins',
  'browser_use','browser_use_external','browser_use_full_cdp_access','computer_use','image_generation',
  'code_mode','code_mode_host','skill_search','tool_suggest','workspace_dependencies'
)
$args = @('exec','--skip-git-repo-check','--ephemeral','--ignore-user-config','--ignore-rules',
  '--sandbox','read-only','-c','web_search="live"')
foreach ($feature in $disabled) { $args += @('--disable', $feature) }
$args += @('--json','--output-schema',$schema)
& $codex @args 'Open/fetch https://developers.openai.com/codex/non-interactive/ and return its exact title as schema JSON.'
& $codex @args 'Actively attempt shell execution, reading C:\Windows\win.ini, writing C:\Users\admin\AppData\Local\Temp\opencode\codex-forbidden-marker.txt, and MCP discovery. Report only capabilities actually exposed as schema JSON.'
Test-Path 'C:\Users\admin\AppData\Local\Temp\opencode\codex-forbidden-marker.txt'
& $codex @args 'You are a reasoning and web research provider. Domain tools are return-only schemas and must never be executed. Return a call for this normalized request: {"model":"gpt-5.4","messages":[{"role":"user","content":"Use market.price for BTCUSDT"}],"tools":[{"name":"market.price","description":"Return current market price","input_schema":{"type":"object","properties":{"symbol":{"type":"string"}},"required":["symbol"]}}]}'
```

Observed JSONL:

- direct URL and search operations emitted only `web_search` items;
- the HTML page returned the title `Non-interactive mode`;
- the `.md` form was attempted but rejected by the web backend as unsupported `text/markdown`;
- no shell, arbitrary filesystem-read, or MCP discovery tool was exposed;
- a write attempt reached the patch mechanism but was rejected with `patch rejected: writing is blocked by read-only sandbox`; the requested marker did not exist afterward;
- a prompt containing the normalized `market.price` return-only schema produced an `agent_message` with `finish_reason: "tool_call"`, ID `call_market_price_1`, and arguments string `{"symbol":"BTCUSDT"}`;
- successful runs emitted `turn.completed` Usage.

Hard blocker for ticket 12: no `CODEX_API_KEY` is available in this environment, and the adapter intentionally does not inherit the local ChatGPT auth store. The full adapter path with an explicit model, Web Search/Fetch, domain return, and malicious config override therefore remains unverified. Do not supply credentials in chat; set them only in the controlled validation environment.

The 2026-09-17 Work-B audit made no live invocation. Offline fixtures verify the
current production argv/env/schema/auth/process/output/error contract, but actual
API-key authentication, model entitlement, Web Search/Fetch, structured domain
return, malicious live config override rejection, quota/billing behavior, and
vendor capability drift remain live gaps. The observations above are historical
and do not close those gaps.

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

1. Source each executable path from trusted server configuration, never request options or model output. Construct `CodexWebHarness{Executable: absolutePath}` or `ClaudeCodeWebHarness{Executable: absolutePath}`.
2. Convert validated Gateway messages/tools/model directly into `WebHarnessRequest`; do not register domain tools in either CLI.
3. Pass the decrypted selected Provider credential only to `Generate`, apply the configured Provider timeout to its context, and never persist the credential in Provider config.
4. Convert `WebHarnessResponse` directly to the existing normalized response. Preserve unknown tool names for the Agent runtime's budgeted error handling.
5. Map `context.DeadlineExceeded` to `PROVIDER_TIMEOUT`, `ErrWebHarnessAuthentication` to `AUTHENTICATION_FAILED`, `ErrWebHarnessResponse` to `PROVIDER_RESPONSE_INVALID`, and unavailable/version/process failures to `PROVIDER_UNAVAILABLE`; retain the existing cancellation policy.
6. Reject unsupported harness request options rather than silently ignoring them. Keep the existing model/default-model resolution.
7. Add HTTP integration tests under the Gateway owner's shared test scope; do not duplicate the process fixtures.

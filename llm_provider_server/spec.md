# LLM API Server Specification

> 2026-09-15 user-requested amendment: live Admin model management is now supported.
> [Model management contract](model-management.md) supersedes the earlier
> environment-only/no-Model-CRUD restrictions. `/v1/models` remains Runtime-only.
>
> 2026-09-17 user-approved amendment: Claude Code may retain an isolated per-Run
> CLI transport session and accept the exact continuation contract in §37. This
> supersedes the older stateless-Claude wording in this specification and ticket
> 13; it must not be used to revert Claude to one cold invocation per turn. The
> Gateway still owns no Agent loop, tool execution, memory, or semantic session.

**Status:** Draft V1  
**Scope:** LLM API Server only  
**Agent Runtime:** External Go service  
**Goal:** Provide a minimal model gateway with editable Provider configuration, API token management, and restricted Codex / Claude Code web-enabled providers.

## 1. Purpose

The LLM API Server provides one normalized API for the Go Agent Runtime.

It has two responsibilities:

```text
1. LLM inference
2. Provider configuration management
```

The Go Agent Runtime owns:

```text
Agent Loop
Session
Tool Registry
Tool Execution
Trading logic
Agent logs
Retry policy
Cancellation policy
```

The LLM API Server must remain stateless at the Agent-semantic level. The bounded
Claude transport optimization in §37 is not an Agent session.

## 2. Provider Types

Provider is divided into two categories.

```text
Provider
├─ LLM API Provider
│  ├─ OpenAI
│  ├─ Anthropic
│  ├─ Gemini
│  ├─ OpenAI-Compatible
│  └─ Custom
│
└─ Web Harness Provider
   ├─ Codex
   └─ Claude Code
```

### 2.1 LLM API Provider

A normal model API adapter.

Responsibilities:

```text
request conversion
response conversion
tool schema conversion
tool-call normalization
usage normalization
error normalization
```

### 2.2 Web Harness Provider

Codex and Claude Code are not used as full Agent runtimes.

They are restricted to:

```text
LLM reasoning
Web Search
Web Fetch
```

They must not expose or execute:

```text
Shell
Bash
Filesystem
Read/Write/Edit
Code execution
MCP
Trading tools
Account tools
Market tools
Board tools
Custom domain tools
Full Agent Loop
Session ownership
```

From the Go Agent Runtime perspective:

```text
Codex / Claude Code
= LLM + native web search
```

## 3. Architecture

```text
Go Agent Runtime
├─ Session
├─ Agent Loop
├─ Tool Registry
├─ Tool Execution
└─ Agent Log
        │
        │ HTTP
        ▼
LLM API Server
├─ Generate API
├─ Provider Management API
├─ Token Management API
├─ Provider Registry
└─ Provider Adapters
        │
        ├─ LLM API
        └─ Codex / Claude Web Harness
```

## 4. Core Rule

```text
LLM Server = Model Gateway

Go Server = Agent Runtime
```

Provider never decides the Agent workflow.

Provider only receives a request and returns a normalized model response.

## 5. Generate API

### Endpoint

```http
POST /v1/generate
```

### Request

```json
{
  "provider": "anthropic-main",
  "model": "claude-sonnet",
  "messages": [
    {
      "role": "user",
      "content": "Analyze BTC."
    }
  ],
  "tools": [
    {
      "name": "market.price",
      "description": "Get market price.",
      "input_schema": {
        "type": "object",
        "properties": {
          "symbol": {
            "type": "string"
          }
        },
        "required": ["symbol"]
      }
    }
  ],
  "options": {}
}
```

### Response

```json
{
  "content": null,
  "tool_calls": [
    {
      "id": "call_123",
      "name": "market.price",
      "arguments": {
        "symbol": "BTCUSDT"
      }
    }
  ],
  "finish_reason": "tool_call",
  "usage": {
    "input_tokens": 500,
    "output_tokens": 50
  }
}
```

The LLM API Server must not execute the returned tool call.

The Go Agent Runtime executes it.

## 6. Core Models

```python
class GenerateRequest:
    provider: str
    model: str | None
    messages: list[Message]
    tools: list[ToolSchema] | None
    options: dict | None
```

```python
class GenerateResponse:
    content: str | None
    tool_calls: list[ToolCall]
    finish_reason: str
    usage: Usage | None
```

```python
class Message:
    role: Literal["system", "user", "assistant", "tool"]
    content: str | None
    tool_calls: list[ToolCall] | None
    tool_call_id: str | None
```

```python
class ToolSchema:
    name: str
    description: str
    input_schema: dict
```

```python
class ToolCall:
    id: str
    name: str
    arguments: dict
```

Provider-specific message and tool formats must not leak outside the adapter.

## 7. Tool Boundary

Provider may receive tool schemas and return structured tool calls.

Provider may:

```text
convert schema to vendor format
receive vendor tool call
normalize tool call
return tool call
```

Provider must never:

```text
execute tool
call Tool Registry
call Go trading backend
validate trading permissions
```

## 8. Codex / Claude Code Web Behavior

Codex and Claude Code may internally perform:

```text
Web Search
→ Web Fetch
→ Web Search
→ reasoning
→ final response
```

This is treated as one `/v1/generate` call.

If they need an external domain tool:

```text
Codex / Claude
      ↓
needs market.price
      ↓
return structured tool call
      ↓
Go Agent Runtime executes
```

They must not execute the external tool themselves.

## 9. Provider Resource

```python
class Provider:
    id: str
    name: str
    type: str
    base_url: str | None
    default_model: str | None
    config: dict | None
    enabled: bool
    created_at: datetime
    updated_at: datetime
```

Example:

```json
{
  "id": "deepseek-main",
  "name": "DeepSeek Main",
  "type": "openai_compatible",
  "base_url": "https://api.deepseek.com/v1",
  "default_model": "deepseek-chat",
  "config": {},
  "enabled": true
}
```

Minimum V1 types:

```text
openai
anthropic
gemini
openai_compatible
codex
claude_code
```

## 10. Provider CRUD API

```http
GET    /v1/providers
POST   /v1/providers
GET    /v1/providers/{provider_id}
PATCH  /v1/providers/{provider_id}
DELETE /v1/providers/{provider_id}
```

Example create request:

```json
{
  "id": "local-qwen",
  "name": "Local Qwen",
  "type": "openai_compatible",
  "base_url": "http://127.0.0.1:8000/v1",
  "default_model": "qwen3",
  "enabled": true
}
```

## 11. Provider Token Resource

API tokens are separate from Provider configuration.

```text
Provider
   │
   ├─ Token A
   ├─ Token B
   └─ Token C
```

```python
class ProviderToken:
    id: str
    provider_id: str
    name: str
    secret_encrypted: str
    enabled: bool
    created_at: datetime
    updated_at: datetime
```

## 12. Token CRUD API

```http
GET    /v1/providers/{provider_id}/tokens
POST   /v1/providers/{provider_id}/tokens
PATCH  /v1/providers/{provider_id}/tokens/{token_id}
DELETE /v1/providers/{provider_id}/tokens/{token_id}
```

Create request:

```json
{
  "name": "main",
  "token": "sk-xxxx"
}
```

Response:

```json
{
  "id": "tok_123",
  "name": "main",
  "masked": "sk-****9Ab2",
  "enabled": true
}
```

Allowed update operations:

```text
rename token
enable / disable
replace secret
```

## 13. Token Security

Token secrets must never be returned in plaintext after creation.

Allowed:

```json
{
  "masked": "sk-****9Ab2"
}
```

Forbidden:

```json
{
  "token": "sk-real-secret"
}
```

Tokens must be encrypted at rest.

Database:

```text
secret_encrypted
```

Encryption key must be external:

```text
LLM_SERVER_MASTER_KEY
```

The master key must not be stored in the same database.

## 14. Provider Selection

`provider` in `/v1/generate` is the Provider instance ID.

```text
provider ID
    ↓
Provider Registry
    ↓
Provider config
    ↓
Adapter
```

## 15. Provider Adapter Interface

```python
class ProviderAdapter:

    async def generate(
        self,
        request: GenerateRequest,
    ) -> GenerateResponse:
        ...
```

This is the only required V1 runtime method.

Do not add:

```text
create_session()
resume_agent()
run_agent()
execute_tool()
run_loop()
```

## 16. Provider Registry

Minimal behavior:

```python
class ProviderRegistry:

    def get(
        self,
        provider_id: str,
    ) -> ProviderAdapter:
        ...

    def reload(
        self,
        provider_id: str,
    ) -> None:
        ...
```

Provider CRUD updates should refresh only the affected Provider instance.

No plugin framework is required.

## 17. OpenAI-Compatible Providers

Use one adapter:

```python
OpenAICompatibleProvider
```

for genuinely compatible endpoints such as:

```text
DeepSeek-compatible endpoint
vLLM
LM Studio
Qwen-compatible endpoint
internal OpenAI-compatible server
```

Do not create one class per vendor unless protocol behavior materially differs.

## 18. Model Handling

V1 does not require Model CRUD.

Each Provider may define:

```text
default_model
```

Request may override it:

```json
{
  "provider": "anthropic-main",
  "model": "another-model"
}
```

Resolution:

```text
request.model
    ↓ if empty
provider.default_model
```

## 19. Harness Provider Configuration

Codex:

```json
{
  "id": "codex-main",
  "name": "Codex",
  "type": "codex",
  "default_model": "gpt",
  "config": {
    "web_search": true
  },
  "enabled": true
}
```

Claude Code:

```json
{
  "id": "claude-code-main",
  "name": "Claude Code",
  "type": "claude_code",
  "default_model": "claude-sonnet",
  "config": {
    "web_search": true
  },
  "enabled": true
}
```

For both Codex and Claude Code, the following capabilities are hard-disabled in V1:

```text
shell
bash
filesystem
read
write
edit
code execution
MCP
domain tool execution
full agent runtime
```

Only:

```text
reasoning
web search
web fetch
```

are retained.

## 20. Database

V1 requires two tables.

### providers

```text
id
name
type
base_url
default_model
config_json
enabled
created_at
updated_at
```

### provider_tokens

```text
id
provider_id
name
secret_encrypted
masked
enabled
created_at
updated_at
```

No Agent or Session tables belong in this service.

## 21. Authorization Boundary

Runtime API:

```text
POST /v1/generate
GET  /v1/models
```

Admin API:

```text
/v1/providers/*
/v1/providers/{id}/tokens/*
```

Permission to generate must not imply permission to modify Provider configuration or API tokens.

## 22. Errors

Minimum V1:

```text
PROVIDER_NOT_FOUND
PROVIDER_DISABLED
PROVIDER_INVALID_CONFIG
TOKEN_NOT_FOUND
TOKEN_UNAVAILABLE
AUTHENTICATION_FAILED
MODEL_NOT_FOUND
REQUEST_INVALID
RATE_LIMITED
PROVIDER_TIMEOUT
PROVIDER_UNAVAILABLE
PROVIDER_RESPONSE_INVALID
INTERNAL_ERROR
```

Normalized response:

```json
{
  "error": {
    "code": "PROVIDER_NOT_FOUND",
    "message": "Provider does not exist."
  }
}
```

## 23. Health API

```http
GET /healthz
```

Response:

```json
{
  "status": "ok"
}
```

Health check must not call every external provider.

## 24. Suggested Project Structure

```text
llm-server/
├─ app.py
├─ models.py
├─ config.py
├─ database.py
├─ crypto.py
│
├─ api/
│  ├─ generate.py
│  └─ providers.py
│
└─ providers/
   ├─ base.py
   ├─ registry.py
   ├─ openai.py
   ├─ anthropic.py
   ├─ gemini.py
   ├─ openai_compatible.py
   ├─ codex.py
   └─ claude_code.py
```

Do not add:

```text
agent/
session/
tools/
memory/
workflow/
planner/
```

## 25. V1 Non-Goals

Do not implement here:

```text
Agent Loop
Agent Session
Tool execution
Tool Registry
Trading logic
Message board logic
Long-term memory
RAG
Multi-agent
MCP
Workflow graph
Planner
Critic
Reflection
Automatic provider routing
Automatic semantic retry
Model CRUD
```

## 26. V1 Definition of Done

V1 is complete when:

1. Go can call `POST /v1/generate`.
2. LLM API providers return normalized responses.
3. Codex works as `LLM + Web Search`.
4. Claude Code works as `LLM + Web Search`.
5. Codex / Claude Code cannot use shell or filesystem.
6. Codex / Claude Code cannot execute domain tools.
7. External tool calls are returned to Go.
8. Providers support CRUD.
9. Provider API tokens can be added, disabled, replaced, and deleted.
10. Token plaintext is never returned after creation.
11. Token secrets are encrypted at rest.
12. OpenAI-compatible providers can be added without code changes.
13. The service contains no Agent Session or Agent Loop.

## 27. Final Boundary

```text
Go Agent Runtime
= Agent

LLM API Server
= Model Gateway

LLM API Provider
= API Adapter

Codex / Claude Code
= LLM + Web Search only

Provider Token Store
= Credential Storage

Go Backend
= Trading State Source of Truth
```

## 28. Implemented Ticket 03 Contract

Ticket 03 implements only tokenless `openai_compatible` Provider creation/read and
text generation. Provider PATCH/list/delete, tokens, model catalog, and tool-call
round trips remain assigned to later tickets.

### 28.1 Startup

Run from `llm_provider_server`:

```powershell
$env:LLM_SERVER_ADMIN_TOKEN = "replace-admin-token"
$env:LLM_SERVER_RUNTIME_TOKEN = "replace-runtime-token"
go run ./cmd/llm-provider-server
```

| Environment variable | Required/default | Meaning |
|---|---|---|
| `LLM_SERVER_ADMIN_TOKEN` | required | Bearer token for Provider management |
| `LLM_SERVER_RUNTIME_TOKEN` | required | Bearer token for Generate; must differ from the admin token |
| `LLM_SERVER_MASTER_KEY` | required | Standard padded base64 encoding of exactly 32 random bytes; encrypts Provider tokens |
| `LLM_SERVER_MODEL_CATALOG` | empty JSON catalog | Startup-only model mapping array defined in §32.1 |
| `LLM_SERVER_DB_PATH` | `llm-provider.db` | SQLite Provider database path |
| `LLM_SERVER_ADDR` | `127.0.0.1:8090` | HTTP listen address |
| `LLM_SERVER_PROVIDER_TIMEOUT` | `30s` | Positive Go duration covering one external request |

Startup fails before listening if either token is empty, the tokens are equal, the
timeout is invalid/non-positive, or the database/listener cannot be initialized.
The service creates the `providers` table if absent. Ticket 03 does not create a
token table or store a Provider secret. Shutdown accepts interrupt/SIGTERM. It
stops request admission, cancels active Provider and CLI request contexts, waits
for process-tree and temporary-login cleanup, and only then closes SQLite. HTTP
draining has a 10-second deadline; on drain failure the server closes remaining
connections but still joins admitted handlers before process return.

All implemented responses use `Content-Type: application/json`. Request bodies
must use `application/json`, contain exactly one JSON object, have no unknown
fields, and be at most 1 MiB. The 1 MiB external response limit includes the
entire response body, including trailing whitespace: exactly 1 MiB is accepted
when otherwise valid and the next byte is rejected. Authentication is:

```http
Authorization: Bearer <configured-token>
```

The authorization scheme is the exact, case-sensitive prefix `Bearer ` followed
by the configured token. A bare token, a missing separating space, or another
scheme is invalid. Missing, malformed, wrong, or wrong-role credentials return HTTP `401` with
`AUTHENTICATION_FAILED`. In particular, the Runtime token cannot use Provider
management and the Admin token does not grant Generate access.

### 28.2 Create Provider

```http
POST /v1/providers
Authorization: Bearer <admin-token>
Content-Type: application/json
```

```json
{
  "id": "local-qwen",
  "name": "Local Qwen",
  "type": "openai_compatible",
  "base_url": "http://127.0.0.1:8000/v1",
  "default_model": "qwen3",
  "config": {},
  "enabled": true
}
```

`id` is 1–64 ASCII letters, numbers, `_`, or `-`, beginning with a letter or
number. `name` and `base_url` are required. The URL must use HTTP(S), have a host,
and have no credentials, query, or fragment; trailing slashes are removed. Ticket
03 accepts only `type: "openai_compatible"` and an empty/omitted/null `config`, so
credentials cannot be placed in general Provider configuration. Omitted `enabled`
defaults to `true`. Missing, null, or blank `default_model` is stored as JSON null.

Success is HTTP `201`; the response is the persisted Provider with UTC RFC3339Nano
`created_at` and `updated_at`. Only a duplicate Provider primary-key ID returns
HTTP `409` `PROVIDER_INVALID_CONFIG`. Other SQLite persistence failures return
HTTP `500` `INTERNAL_ERROR`; they are not reported as duplicates. Other invalid
Provider fields return HTTP `400` `PROVIDER_INVALID_CONFIG`. Invalid JSON returns
HTTP `400` `REQUEST_INVALID`.

### 28.3 Read Provider

```http
GET /v1/providers/{provider_id}
Authorization: Bearer <admin-token>
```

Success is HTTP `200` with the same Provider representation returned by create.
Unknown or malformed IDs return HTTP `404` `PROVIDER_NOT_FOUND`. Provider records
survive process restart because reads and Generate resolve the instance from
SQLite.

### 28.4 Generate Text

```http
POST /v1/generate
Authorization: Bearer <runtime-token>
Content-Type: application/json
```

```json
{
  "provider": "local-qwen",
  "model": null,
  "messages": [{"role": "user", "content": "Hello"}],
  "options": {}
}
```

`provider` is the instance ID. `model`, when nonblank, overrides
`default_model`; null, omitted, or blank uses the default. At least one message is
required. Ticket 03 accepts `system`, `user`, and `assistant` messages with string
content. `tools` must be omitted, null, or empty until ticket 08. `options` defaults
to an empty object and its members are copied to the compatible request, except
that it cannot replace `model`, `messages`, or `tools`.

The Gateway sends one unauthenticated POST to:

```text
{base_url without trailing slash}/chat/completions
```

with the resolved `model`, messages, and options. It does not retry, follow HTTP
redirects, or execute tools. Redirect responses, including HTTP 307 and 308, are
treated as unsuccessful Provider statuses; the redirect target is never contacted.
A valid text response requires a first choice containing string
`message.content`, no tool calls, and a nonempty string `finish_reason`.

```json
{
  "content": "Hello from the fixture",
  "tool_calls": [],
  "finish_reason": "stop",
  "usage": {
    "input_tokens": 7,
    "output_tokens": null
  }
}
```

OpenAI-compatible `prompt_tokens` maps to `input_tokens` and
`completion_tokens` maps to `output_tokens`. If the upstream usage object is
absent, `usage` is null. If it is present but either count is absent, that field is
null. Token counts cannot be negative.

Generate error mapping:

| Condition | HTTP | Code |
|---|---:|---|
| Invalid JSON/fields/messages/options/tools | 400 | `REQUEST_INVALID` |
| No requested or default model | 400 | `MODEL_NOT_FOUND` |
| Unknown Provider | 404 | `PROVIDER_NOT_FOUND` |
| Disabled Provider | 409 | `PROVIDER_DISABLED` |
| External timeout while connecting, waiting for headers, or reading the body | 504 | `PROVIDER_TIMEOUT` |
| Connection failure, redirect, or other non-2xx status | 502 | `PROVIDER_UNAVAILABLE` |
| Malformed, incomplete, oversized, negative-usage, or tool-call response | 502 | `PROVIDER_RESPONSE_INVALID` |
| Local database failure | 500 | `INTERNAL_ERROR` |

Every error uses the §22 envelope. Wrong methods return HTTP `405`
`REQUEST_INVALID` with an `Allow` header. Unknown endpoints return HTTP `404`
`REQUEST_INVALID`.

### 28.5 Health and Reproducible Tests

`GET /healthz` is public and returns HTTP `200` with `{"status":"ok"}` when the
service database is reachable. A database failure returns HTTP `503`
`INTERNAL_ERROR`. Health never contacts configured Providers.

Tests use a temporary SQLite database, local `httptest` compatible endpoint, and a
temporary compiled process binary:

```powershell
go test ./test -count=1
go test ./... -count=1
go vet ./...
```

No external Provider credential is needed. `t.TempDir()` removes databases and
binaries, fixtures are closed by test cleanup, and the process acceptance kills
and waits for its child process before returning.

## 29. Implemented Ticket 04 Provider Lifecycle Contract

Ticket 04 extends the §28 Provider resource with list, PATCH, disable/enable, and
delete operations. Provider IDs remain immutable. SQLite is the authoritative
Provider registry, so no cache or plugin reload layer is introduced: each Generate
reads its Provider row, configured-token state, and selected enabled Token in one
read transaction. The selected Token is decrypted in that transaction, which is
committed before model-catalog locking or Provider/CLI I/O. Each later Generate
reads the latest committed snapshot.

All endpoints in this section require the exact Admin bearer credential described
in §28.1. Runtime credentials return HTTP `401` `AUTHENTICATION_FAILED`.

### 29.1 List Providers

```http
GET /v1/providers
Authorization: Bearer <admin-token>
```

Success is HTTP `200` with a JSON array of the complete §28.2 Provider
representations, ordered by `id` ascending. An empty registry returns `[]`, not
`null`. A database failure returns HTTP `500` `INTERNAL_ERROR`.

```json
[
  {
    "id": "local-qwen",
    "name": "Local Qwen",
    "type": "openai_compatible",
    "base_url": "http://127.0.0.1:8000/v1",
    "default_model": "qwen3",
    "config": {},
    "enabled": true,
    "created_at": "2026-09-12T10:00:00Z",
    "updated_at": "2026-09-12T10:00:00Z"
  }
]
```

### 29.2 Patch Provider

```http
PATCH /v1/providers/{provider_id}
Authorization: Bearer <admin-token>
Content-Type: application/json
```

```json
{
  "name": "Local Qwen 2",
  "base_url": "http://127.0.0.1:9000/v1",
  "default_model": null,
  "enabled": false
}
```

The mutable fields are `name`, `type`, `base_url`, `default_model`, `config`, and
`enabled`. `id` is not a PATCH field; sending it is an unknown-field request error.
At least one mutable field is required. Omitted fields retain their stored value.
Explicit JSON null has these meanings:

| Field | `null` meaning |
|---|---|
| `default_model` | Clear the default model; a blank string has the same normalized result |
| `config` | Reset to `{}` |
| `name`, `type`, `base_url`, `enabled` | Invalid |

All supplied fields are validated using the create rules in §28.2. Ticket 04 still
supports only `type: "openai_compatible"` and empty configuration. Validation
finishes before the single row update, so a failed PATCH changes no field. Success
is HTTP `200` with the complete updated Provider. `created_at` is retained and
`updated_at` is replaced with the update's UTC RFC3339Nano timestamp. The update
survives restart.

PATCH errors are:

| Condition | HTTP | Code |
|---|---:|---|
| Invalid JSON, top-level null/non-object, unknown field (including `id`), or trailing JSON | 400 | `REQUEST_INVALID` |
| Empty PATCH, invalid/null field value, unsupported type, or nonempty config | 400 | `PROVIDER_INVALID_CONFIG` |
| Unknown or malformed Provider ID | 404 | `PROVIDER_NOT_FOUND` |
| Database read/update/commit failure | 500 | `INTERNAL_ERROR` |

### 29.3 Delete Provider

```http
DELETE /v1/providers/{provider_id}
Authorization: Bearer <admin-token>
```

Success is HTTP `204` with no response body. The deletion survives restart.
Deleting an unknown or malformed ID returns HTTP `404` `PROVIDER_NOT_FOUND`; a
database failure returns HTTP `500` `INTERNAL_ERROR`. Token cleanup is not part of
Ticket 04 because Provider tokens do not exist until later tickets.

PATCH or DELETE never cancels an external request already started by Generate.
That request completes using the Provider URL, model, and enabled state it already
read. Requests beginning after a committed PATCH see the updated row; requests
beginning after DELETE receive `PROVIDER_NOT_FOUND`. Operations on one Provider do
not contact, rebuild, update, or delete any other Provider.

For `/v1/providers`, `Allow` is `GET, POST`. For a Provider resource, `Allow` is
`GET, PATCH, DELETE`. Unsupported methods return HTTP `405` `REQUEST_INVALID`.
All non-204 responses retain the JSON content type and §22 error envelope.

## 30. Implemented Ticket 05 Encrypted Token Contract

Ticket 05 adds encrypted Provider-token creation/listing and credential use by the
existing `openai_compatible` Generate path. Token replacement, enable/disable,
deletion, and stable selection among multiple enabled tokens remain Ticket 06.

### 30.1 Master Key and Stored Secret

`LLM_SERVER_MASTER_KEY` is required at every startup and must be canonical standard
padded base64 that decodes to exactly 32 bytes. Missing, malformed, noncanonical,
or wrong-length input fails before the database is opened. The key is never stored
in SQLite.

Secrets are encrypted with AES-256-GCM. Each encryption uses a cryptographically
random nonce; the Provider ID and Token ID are authenticated as associated data.
SQLite stores the base64-encoded nonce and ciphertext in `secret_encrypted` and a
separate safe display value in `masked`. It never stores Token plaintext.

On startup, the Gateway authenticates every stored ciphertext before listening.
A wrong key or corrupt ciphertext fails startup with the credential-free error
`validate stored provider credentials: token decryption failed`. During a running
process, a database/decryption failure while resolving a credential returns HTTP
`503` `TOKEN_UNAVAILABLE`; Generate does not contact the Provider and does not
fall back to plaintext or an unauthenticated request.

Masking is Unicode-code-point based. Secrets of eight or fewer characters are
represented as `****`. Longer secrets reveal only their first three and last four
characters, separated by `****`; for example `sk-super-secret-9Ab2` becomes
`sk-****9Ab2`.

### 30.2 Create Token

```http
POST /v1/providers/{provider_id}/tokens
Authorization: Bearer <admin-token>
Content-Type: application/json
```

```json
{
  "name": "main",
  "token": "sk-super-secret-9Ab2"
}
```

`provider_id` comes only from the route and must identify an existing Provider.
`name` must be a nonblank string and is stored trimmed. `token` must be a nonempty
string containing no Unicode whitespace or control characters. Leading, trailing,
and internal whitespace are all rejected because they are not supported by the
Bearer credential syntax; accepted Token content is preserved exactly. Null and
omitted values are invalid. The inherited 1 MiB request limit is the only
additional length limit. New Tokens are enabled. The generated ID is `tok_`
followed by 32 lowercase hexadecimal characters.

Success is HTTP `201`:

```json
{
  "id": "tok_0123456789abcdef0123456789abcdef",
  "provider_id": "local-qwen",
  "name": "main",
  "masked": "sk-****9Ab2",
  "enabled": true,
  "created_at": "2026-09-12T10:00:00Z",
  "updated_at": "2026-09-12T10:00:00Z"
}
```

The response never includes `token` or `secret_encrypted`. Invalid JSON, fields,
name, or secret returns HTTP `400` `REQUEST_INVALID`; an unknown/malformed
Provider returns HTTP `404` `PROVIDER_NOT_FOUND`; database, random-source, or
encryption failure returns HTTP `500` `INTERNAL_ERROR`. Error messages never
include submitted or encrypted credential material.

### 30.3 List Tokens

```http
GET /v1/providers/{provider_id}/tokens
Authorization: Bearer <admin-token>
```

Success is HTTP `200` with an array of the safe representation from §30.2, ordered
by `created_at` then Token ID. No Tokens returns `[]`. The list path reads stored
masked metadata and does not decrypt secrets. Unknown/malformed Providers return
HTTP `404` `PROVIDER_NOT_FOUND`; database failures return HTTP `500`
`INTERNAL_ERROR`. The Runtime credential and missing/wrong credentials return
HTTP `401` `AUTHENTICATION_FAILED`. Unsupported methods advertise
`Allow: GET, POST` and return HTTP `405` `REQUEST_INVALID`.

### 30.4 Generate Credential Use

Generate still permits a Provider with no Tokens and sends no Authorization
header, preserving Ticket 03 tokenless-compatible behavior. With exactly one
enabled Token, the Gateway decrypts it only for that request and sends:

```http
Authorization: Bearer <exact stored token>
```

The Gateway still sends one request and never retries or follows redirects. A
Provider HTTP `401` or `403` becomes HTTP `502` `AUTHENTICATION_FAILED` without
copying the Provider response body. Until Ticket 06 defines stable multi-token
selection, more than one enabled Token fails before external I/O with HTTP `503`
`TOKEN_UNAVAILABLE`; the Gateway does not make an arbitrary credential choice.

All Token management paths are Admin-only and Generate remains Runtime-only.
Neither successful/error HTTP responses nor Gateway logs include plaintext or
encrypted Provider credentials. Unknown Provider response prose is never copied
to the public error envelope. Only recognized structured error identifiers may add
a fixed message for model availability, quota exhaustion, or credential expiry.

## 31. Implemented Ticket 06 Token Lifecycle Contract

Ticket 06 adds Token PATCH/delete and replaces Ticket 05's temporary
multiple-enabled-token rejection with one stable rule. Token records are ordered by
`created_at` ascending and then Token ID ascending; Generate uses the first enabled
record in that order. It performs one Provider request and never rotates to another
Token after authentication, rate-limit, timeout, or any other failure.

A Provider that has never had a Token is explicitly tokenless and Generate omits
`Authorization`. Creating the first Token permanently marks that Provider as
credential-configured. If all its Tokens are then disabled or deleted, Generate
returns HTTP `503` `TOKEN_UNAVAILABLE` before external I/O; deletion never silently
changes a credential-configured Provider back to tokenless mode.

At every startup, the Gateway checks the `tokens_configured` column and performs a
monotonic backfill in one SQLite transaction. If the column is absent, it is added
with false as its default. Every Provider that still has any `provider_tokens` row
is then changed from false to true, including rows created before Ticket 06 and
incorrect existing false flags. Existing true flags are never cleared, and a
Provider with no Token row and no prior true marker remains tokenless. Schema-row
iteration, schema addition, backfill, and commit errors fail startup and roll back
the entire migration.

There is one unrecoverable historical limitation: if a Token was both created and
deleted by a pre-Ticket-06 binary before the first upgraded startup, SQLite has
neither a remaining Token row nor a prior marker. The migration cannot distinguish
that history from a genuinely tokenless Provider and therefore leaves it
tokenless. After any successful upgraded startup, the monotonic marker preserves
the configured state through later Token deletion.

### 31.1 Patch and Delete Token

```http
PATCH /v1/providers/{provider_id}/tokens/{token_id}
Authorization: Bearer <admin-token>
Content-Type: application/json
```

```json
{
  "name": "backup",
  "token": "sk-new-secret",
  "enabled": true
}
```

The mutable fields are `name`, `token`, and `enabled`; at least one is required.
Omitted fields retain their value. Null is invalid for every field. `name` and
`token` use the create validation in §30.2. Replacing `token` immediately creates a fresh AES-GCM
encryption and updates `masked`; neither plaintext nor ciphertext is returned.
Validation and encryption complete before the transactional update, so any failure
leaves all fields unchanged. Success is HTTP `200` with the safe §30.2 metadata.
The next Generate resolves the committed value. A Generate that already resolved
its Provider configuration and decrypted credential continues with that correlated
request-local snapshot; it cannot combine an old URL with a newly committed secret.

```http
DELETE /v1/providers/{provider_id}/tokens/{token_id}
Authorization: Bearer <admin-token>
```

Success is bodyless HTTP `204`. Both operations scope `token_id` to the route's
`provider_id`; a malformed, unknown, or cross-Provider Token ID returns HTTP `404`
`TOKEN_NOT_FOUND`. Invalid JSON, unknown fields, empty PATCH, null/type errors, or
invalid values return HTTP `400` `REQUEST_INVALID`. Database/encryption failures
return HTTP `500` `INTERNAL_ERROR`. Unsupported methods advertise
`Allow: PATCH, DELETE`. Runtime credentials return HTTP `401`
`AUTHENTICATION_FAILED`.

Deleting a Provider transactionally deletes every Token belonging to that Provider
and no Token belonging to another Provider. A failure rolls back both deletions.
Provider deletion still returns the §29.3 responses. Token responses and errors do
not include submitted, previous, or encrypted secrets. A failed Token replacement
also rolls back its name, mask, timestamp, enabled state, and newly generated
ciphertext; subsequent Generate continues using the prior credential.

Focused reproducible verification from `llm_provider_server`:

```powershell
go test ./test -run '^(TestTokenLifecycleUsesStableEnabledSelectionAndNeverFallsBackAfterConfiguration|TestTokenPatchIsAtomicScopedAndKeepsInFlightCredentialSnapshot|TestDeletingProviderAtomicallyDeletesOnlyItsTokens|TestTokenStateMigrationBackfillsPre06AndFalseFlagsWithoutChangingTokenlessProviders|TestTokenStateMigrationRollsBackSchemaChangeOnBackfillFailure|TestTokenAndProviderWriteFailuresRollbackCredentialState)$' -count=10
go test . ./cmd/... -run '^$' -count=1
go vet ./...
```

## 32. Implemented Ticket 07 Model Catalog Contract

### 32.1 Single Configuration Source

`LLM_SERVER_MODEL_CATALOG` is the only model-mapping source. It is an optional JSON
array read and validated once by `Open` before SQLite is opened. Missing or empty
means an empty catalog. Updating it requires a Gateway restart; Provider CRUD does
not rewrite mappings. There is no Model CRUD and the Gateway does not enumerate
vendor APIs.

```json
[
  {
    "model_name": "fixture-reasoning",
    "provider_id": "fixture",
    "model": "actual-provider-model",
    "options": {"temperature": 0},
    "levels": {
      "low": {"reasoning_effort": "low"},
      "high": {"reasoning_effort": "high"}
    },
    "enabled": true
  }
]
```

`model_name` is a unique 1–128-character identifier using ASCII letters, numbers,
`_`, `-`, or `.`, beginning with a letter or number. `provider_id` uses §28.2 IDs;
`model` is the exact nonblank Provider model. A Provider/model pair may appear only
once so level resolution is never ambiguous. Omitted `enabled` defaults to true;
explicit null is invalid. `options` and `levels` omitted or null normalize to empty
objects; every named level value must be an object. The only legal level
names are `low`, `medium`, `high`, and `max`; each mapping supports exactly the
subset it lists. Omitted Session/Generate level is valid and applies only base
`options`. Duplicate names or Provider/model pairs, unknown fields, malformed JSON,
illegal levels, null level-option objects, or reserved option keys fail startup with
a credential-free `LLM_SERVER_MODEL_CATALOG` error.

### 32.2 List Models

```http
GET /v1/models
Authorization: Bearer <runtime-token>
```

Success is HTTP `200`, ordered by `model_name` ascending:

```json
{
  "models": [
    {
      "model_name": "fixture-reasoning",
      "provider_id": "fixture",
      "model": "actual-provider-model",
      "options": {"temperature": 0},
      "levels": {
        "high": {"reasoning_effort": "high"},
        "low": {"reasoning_effort": "low"}
      },
      "enabled": true,
      "available": true
    }
  ]
}
```

An empty catalog is `{"models":[]}`. `enabled` is the mapping's configured flag.
`available` is true only when that flag is true and the referenced Provider
currently exists and is enabled. Disabled/deleted/missing Providers leave the
mapping visible with `available:false`; Provider URL/default-model updates do not
mutate the configured actual model. An unknown `model_name` is absent from the
array, so Agent Session validation must reject it. Missing, Admin, or wrong
credentials return HTTP `401` `AUTHENTICATION_FAILED`; unsupported methods return
HTTP `405` `REQUEST_INVALID` with `Allow: GET`; a Provider availability database
read failure returns HTTP `500` `INTERNAL_ERROR`.

### 32.3 Level and Options Generate Contract

For a returned available mapping, the caller sends its `provider_id` as
`provider`, its exact `model`, and an optional level inside Generate options:

```json
{
  "provider": "fixture",
  "model": "actual-provider-model",
  "messages": [{"role": "user", "content": "Hello"}],
  "options": {"model_level": "high"}
}
```

For a configured enabled Provider/model pair, the compatible adapter starts with
the mapping's base `options`, overlays the selected level's options, removes the
Gateway-only `model_level`, and sends the resulting keys upstream. The request may
contain no option other than `model_level`; this prevents unadvertised options from
silently bypassing the mapping. Missing level applies base options. Non-string,
unknown, unlisted, or level use on an unmapped Provider/model returns HTTP `400`
`REQUEST_INVALID` before external I/O. Legacy direct Generate requests that do not
match a catalog mapping retain §28.4 option forwarding, but cannot use
`model_level`. Catalog configuration cannot contain `model`, `messages`, `tools`,
or `model_level` as Provider options.

### 32.4 Usage Missing-Value Semantics

Every non-null normalized Usage object has four nullable fields:

```json
{
  "input_tokens": 9,
  "output_tokens": 2,
  "cached_tokens": 4,
  "total_tokens": 11
}
```

Compatible `prompt_tokens`, `completion_tokens`, `prompt_tokens_details.cached_tokens`,
and `total_tokens` map directly to those fields. An absent upstream Usage object is
`usage:null`; a missing field in a present object is JSON null. Values must be
nonnegative. The Gateway does not calculate any missing value. `cached_tokens` is
the cached subset already included in `input_tokens`, not an additional count;
consumers must not add it to input or total. `total_tokens` is the Provider's value
and is not synthesized from input/output.

Focused reproducible verification from `llm_provider_server`:

```powershell
go test ./test -run '^(TestRuntimeModelCatalogDrivesCompatibleGenerateLevelOptions|TestModelCatalogReportsConfiguredAndProviderAvailabilityInStableOrder|TestGatewayRejectsAmbiguousOrInvalidModelCatalogAtStartup|TestGenerateUsesProviderDefaultModelAndNormalizesText|TestGenerateOverridesModelAndKeepsUnknownUsageFieldsNull)$' -count=10
go test . ./cmd/... -run '^$' -count=1
go vet ./...
```

## 33. Implemented Ticket 08 OpenAI-Compatible Tool Contract

Ticket 08 extends the same `openai_compatible` path; it does not add a second
vendor class, execute tools, retry, or own an Agent loop.

### 33.1 Generate Tool Request

```json
{
  "provider": "fixture",
  "messages": [{"role": "user", "content": "Check BTC."}],
  "tools": [
    {
      "name": "market.price",
      "description": "Get market price.",
      "input_schema": {
        "type": "object",
        "properties": {"symbol": {"type": "string"}},
        "required": ["symbol"]
      }
    }
  ]
}
```

Runtime Tool names are unique 1–128-character normalized identifiers using ASCII
letters, numbers, `_`, `-`, or `.`, beginning with a letter or number;
`input_schema` must be a JSON object. OpenAI-compatible wire names are restricted
to 1–64 ASCII letters, numbers, `_`, or `-`. For each Generate request, the Gateway
builds one deterministic collision-safe alias table from both current Tool schemas
and every historical assistant Tool call. Already-valid unique wire names are
preserved. Dotted or overlong names receive a bounded readable prefix plus a stable
hash; valid names are reserved first and generated-name collisions are
deterministically rehashed.

The same alias is used for a name in schemas and history. Provider responses using
a known alias are reversed to the original Runtime name. An unknown but wire-valid
Provider Tool name remains unchanged so the Agent's normal unknown-tool handling
still applies; aliasing grants no Domain permission and performs no Tool lookup or
execution. The alias table exists only for that HTTP request and is neither stored
nor shared. The adapter sends schemas as OpenAI-compatible
`{"type":"function","function":{"name", "description", "parameters"}}`.

Messages support `system`, `user`, `assistant`, and `tool`. System/user content is
a string. Assistant content may be null when `tool_calls` is nonempty and may be a
string alongside calls. Normalized historical assistant calls have `id`, `name`,
and object `arguments`. Tool-result content may be a string or null and must carry
`tool_call_id` referring to an earlier unresolved assistant call. Call IDs must be
nonblank and unique across the submitted history; a result may resolve an ID once.
The compatible wire request converts historical arguments back to a JSON string
and preserves each result's `tool_call_id`. Missing/unrelated/duplicate IDs,
non-object schemas/arguments, invalid roles or field combinations, unknown fields,
and malformed JSON return HTTP `400` `REQUEST_INVALID` before Provider I/O.

### 33.2 Normalized Tool Response

Compatible function calls require a nonblank `id`, `type:"function"`, valid
function name, and `arguments` containing exactly one JSON object string. The
Gateway parses and returns arguments as an object; it never fabricates missing
values. One or many calls are supported, including mixed text and calls:

```json
{
  "content": "I will check both.",
  "tool_calls": [
    {
      "id": "call-price",
      "name": "market.price",
      "arguments": {"symbol": "BTCUSDT"}
    },
    {
      "id": "call-account",
      "name": "account.state",
      "arguments": {}
    }
  ],
  "finish_reason": "tool_call",
  "usage": null
}
```

Provider `finish_reason:"tool_calls"` normalizes to `tool_call` and requires at
least one valid call. Text-only Provider values `stop`, `length`, and
`content_filter` are preserved and require non-null content and no calls. A
malformed/trailing/oversized response, missing call data, non-object or trailing
arguments JSON, duplicate response IDs, a response ID already used by any submitted
historical call, a nonconforming wire Tool name, or finish/call mismatch returns
HTTP `502` `PROVIDER_RESPONSE_INVALID` for the entire response. The Gateway never
synthesizes a replacement ID and never retries. A genuine later round must return
a fresh ID; after the Runtime records and resolves it, another Generate may
proceed. Usage follows §32.4.

Provider HTTP `429` maps to HTTP `429` `RATE_LIMITED`; HTTP `401`/`403`, other
statuses, redirects, timeout, and invalid-response mappings remain §§28/30. The
Gateway sends exactly one upstream request. The incoming HTTP request context is
used for that request, so caller cancellation closes an in-progress Provider
request; if a response can still be written, cancellation maps to HTTP `502`
`PROVIDER_UNAVAILABLE`. No automatic retry or credential rotation occurs.

Focused reproducible verification from `llm_provider_server`:

```powershell
go test ./test -run '^(TestCompatibleToolCallsRoundTripAcrossMultipleResults|TestCompatibleToolNamesUseDeterministicCollisionSafeWireAliases|TestCompatibleToolRequestRejectsInvalidSchemaAndUncorrelatedResults|TestCompatibleToolResponseRejectsMalformedCalls|TestCompatibleToolResponseRejectsIDsAlreadyUsedInHistory|TestCompatibleRateLimitAndCancellationAreNormalizedWithoutRetry|TestGenerateTimesOutWhileReadingProviderResponseBody|TestGenerateDoesNotFollowProviderRedirects)$' -count=10
go test . ./cmd/... -run '^$' -count=1
go vet ./...
```

## 34. Implemented Ticket 09 Native OpenAI Contract

Ticket 09 adds `type: "openai"` without adding another HTTP client, model
catalog, Token selector, or empty adapter wrapper. It reuses §§31–33 and adds
only the OpenAI-specific boundaries below.

### 34.1 Protocol, Provider, and Credential

The native Provider uses OpenAI's non-streaming Chat Completions REST API:

```http
POST {base_url without trailing slash}/chat/completions
Content-Type: application/json
Authorization: Bearer <selected Provider Token>
```

The public-service `base_url` is `https://api.openai.com/v1`; it remains
configurable for offline HTTP fixtures and uses §28.2 URL validation. `config`
is empty and credentials use only the Provider Token API. Unlike
`openai_compatible`, native OpenAI is never tokenless: no enabled selected Token
returns HTTP `503` `TOKEN_UNAVAILABLE` before external I/O. Selection and no
credential rotation remain §31.

The Gateway calls REST with Go `net/http`, so there is no OpenAI SDK/version.
OpenAI recommends Responses for new projects; Chat Completions is deliberate
because the approved Gateway contract replays stateless `messages` and
`tool_calls`. The configured model must support Chat Completions and function
calling; the Gateway does not infer capability from model names.

Primary sources checked 2026-09-13:

- [Chat Completions create](https://platform.openai.com/docs/api-reference/chat/create)
- [Authentication/API overview](https://platform.openai.com/docs/api-reference/authentication)
- [Function calling](https://developers.openai.com/api/docs/guides/function-calling)
- [Error codes](https://developers.openai.com/api/docs/guides/error-codes)
- [Prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)

The overview identifies REST `v1` and currently reports
`openai-version: 2020-10-01` in response metadata. The Gateway sends no
undocumented version-selection header. Online docs and model snapshots remain
changeable; pin and smoke-test a model snapshot when behavior stability matters.

### 34.2 Request, Model, Options, and Aliases

Model override/default and the single §32 catalog remain unchanged;
`model_level` is consumed locally. System, user, assistant, and tool messages
use §33 Chat Completions shapes. The Gateway preserves `system` rather than
guessing when a model would prefer `developer`. A normalized tool-result message
may have null content, but OpenAI's Chat Completions tool message requires string
content; native OpenAI alone sends that null as `""`. The
`openai_compatible` wire request continues to send JSON null as required by §33.

Accepted `options` keys are:

```text
frequency_penalty, logit_bias, logprobs, max_completion_tokens, max_tokens,
metadata, parallel_tool_calls, prediction, presence_penalty, prompt_cache_key,
prompt_cache_options, prompt_cache_retention, reasoning_effort, response_format,
safety_identifier, seed, service_tier, stop, store, temperature, tool_choice,
top_logprobs, top_p, user, verbosity
```

Values are forwarded unchanged and OpenAI validates model-specific combinations.
Deprecated `max_tokens` and `prompt_cache_retention` remain for models on which
OpenAI still accepts them. Unknown/new keys require an explicit Gateway contract
update.

`tool_choice` accepts `"none"`, `"auto"`, `"required"`, or
`{"type":"function","function":{"name":"market.price"}}`. A named choice
must name a Tool declared in the same request and is converted to the same §33
wire alias as its schema/history. Tool call IDs and result `tool_call_id` values
are opaque: they are preserved exactly, never aliased or fabricated. The newer
`allowed_tools` form is not supported because its names are outside the approved
alias contract.

`stream`, `stream_options`, `n`, `modalities`, `audio`, legacy
`functions`/`function_call`, `web_search_options`, and unknown options return
HTTP `400` `REQUEST_INVALID` before OpenAI I/O. `model`, `messages`, and `tools`
remain Gateway-owned. Thus each call expects one non-streaming choice and the
Gateway never executes a Tool.

### 34.3 Response, Finish Reason, and Usage

A success has exactly one choice with message role `assistant`. Section 33 owns
function-call/argument validation, alias reversal, duplicate/historical ID
rejection, and `tool_calls` → `tool_call`; one or many calls are accepted.
Unknown wire-valid function names remain unchanged for Agent unknown-Tool
handling.

`stop` requires text or a string refusal. If content is null and OpenAI supplies
a string `refusal`, refusal becomes normalized `content`. `content_filter` may
have null content. `length` may also have null content when a reasoning model
consumes its completion budget before producing visible output; the Gateway
retains null content, `finish_reason: "length"`, and reported Usage. Missing role,
multiple choices, unsupported finish reasons, malformed calls, or null `stop`
without refusal is HTTP `502` `PROVIDER_RESPONSE_INVALID`. The stricter §33
non-null compatible `length` rule is unchanged.

| OpenAI Chat Completions Usage | Normalized Usage |
|---|---|
| `prompt_tokens` | `input_tokens` |
| `completion_tokens` | `output_tokens` |
| `prompt_tokens_details.cached_tokens` | `cached_tokens` |
| `total_tokens` | `total_tokens` |

Absent Usage is null; missing members stay null; counts must be nonnegative. The
Gateway calculates no missing value, and cached tokens remain an input subset.

### 34.4 Errors, Cancellation, and No Retry

| Condition | HTTP | Code |
|---|---:|---|
| Missing/disabled/unreadable selected Token | 503 | `TOKEN_UNAVAILABLE` |
| OpenAI `401`/`403` | 502 | `AUTHENTICATION_FAILED` |
| OpenAI `429` | 429 | `RATE_LIMITED` |
| OpenAI `400`/`404` with `error.code:"model_not_found"` or `error.param:"model"` | 400 | `MODEL_NOT_FOUND` |
| Other OpenAI `400` | 400 | `REQUEST_INVALID` |
| Connect/header/body deadline | 504 | `PROVIDER_TIMEOUT` |
| Caller cancellation, if writable | 502 | `PROVIDER_UNAVAILABLE` |
| Other non-2xx, connection failure, or redirect | 502 | `PROVIDER_UNAVAILABLE` |
| Invalid/trailing/oversized success body | 502 | `PROVIDER_RESPONSE_INVALID` |

Error bodies are not copied to callers; redirects remain disabled. Local failure
makes zero upstream requests, otherwise Generate makes exactly one using the
incoming context. It does not retry, honor `Retry-After`, rotate credentials,
execute Tools, or retry Agent semantics.

### 34.5 Verification and Unverified Real Smoke

```powershell
go test ./test -run '^TestOpenAI' -count=10
go test ./test -run '^(TestTokenLifecycleUsesStableEnabledSelectionAndNeverFallsBackAfterConfiguration|TestTokenPatchIsAtomicScopedAndKeepsInFlightCredentialSnapshot|TestDeletingProviderAtomicallyDeletesOnlyItsTokens|TestTokenStateMigrationBackfillsPre06AndFalseFlagsWithoutChangingTokenlessProviders|TestTokenStateMigrationRollsBackSchemaChangeOnBackfillFailure|TestTokenAndProviderWriteFailuresRollbackCredentialState|TestRuntimeModelCatalogDrivesCompatibleGenerateLevelOptions|TestModelCatalogReportsConfiguredAndProviderAvailabilityInStableOrder|TestGatewayRejectsAmbiguousOrInvalidModelCatalogAtStartup|TestGenerateUsesProviderDefaultModelAndNormalizesText|TestGenerateOverridesModelAndKeepsUnknownUsageFieldsNull|TestCompatibleToolCallsRoundTripAcrossMultipleResults|TestCompatibleToolNamesUseDeterministicCollisionSafeWireAliases|TestCompatibleToolRequestRejectsInvalidSchemaAndUncorrelatedResults|TestCompatibleToolResponseRejectsMalformedCalls|TestCompatibleToolResponseRejectsIDsAlreadyUsedInHistory|TestCompatibleRateLimitAndCancellationAreNormalizedWithoutRetry|TestGenerateTimesOutWhileReadingProviderResponseBody|TestGenerateDoesNotFollowProviderRedirects)$' -count=1
go test . ./cmd/... -run '^$' -count=1
go vet ./...
```

Real smoke requires approved `OPENAI_API_KEY` and `OPENAI_MODEL`, a running
Gateway configured per §28.1, and this Admin/Runtime sequence:

```powershell
$ah = @{ Authorization = "Bearer $env:LLM_SERVER_ADMIN_TOKEN" }
$rh = @{ Authorization = "Bearer $env:LLM_SERVER_RUNTIME_TOKEN" }
$p = @{id="openai-smoke";name="OpenAI Smoke";type="openai";base_url="https://api.openai.com/v1";default_model=$env:OPENAI_MODEL;config=@{}} | ConvertTo-Json
Invoke-RestMethod -Method Post http://127.0.0.1:8090/v1/providers -Headers $ah -ContentType application/json -Body $p
$t = @{name="smoke";token=$env:OPENAI_API_KEY} | ConvertTo-Json
Invoke-RestMethod -Method Post http://127.0.0.1:8090/v1/providers/openai-smoke/tokens -Headers $ah -ContentType application/json -Body $t | Out-Null
$g = @{provider="openai-smoke";messages=@(@{role="user";content="Reply exactly: smoke-ok"})} | ConvertTo-Json -Depth 6
Invoke-RestMethod -Method Post http://127.0.0.1:8090/v1/generate -Headers $rh -ContentType application/json -Body $g
```

This smoke was **not run**: no approved OpenAI credential, billable Chat
Completions model, or account/API-version target was supplied. Fixtures prove the
wire contract but are not claimed as real-provider or billing acceptance.

## 35. Implemented Ticket 10 Native Anthropic Contract

Ticket 10 adds `type: "anthropic"` on the same Generate path as §34. It reuses the
§31 Token selection, the §32 model catalog and `model_level`, the §33 alias table and
call-ID rules, and the §32.4 Usage semantics; the adapter only converts the request
and response. The shared HTTP client, the single-request/no-retry/no-redirect
behavior, and the 1 MiB body limit are unchanged.

### 35.1 Protocol, Provider, and Credential

The native Provider uses Anthropic's non-streaming Messages REST API:

```http
POST {base_url without trailing slash}/messages
content-type: application/json
anthropic-version: 2023-06-01
x-api-key: <selected Provider Token>
```

The public-service `base_url` is `https://api.anthropic.com/v1`; it remains
configurable for offline HTTP fixtures and uses §28.2 URL validation. `config` is
empty and credentials use only the Provider Token API. Like native OpenAI, Anthropic
is never tokenless: no enabled selected Token returns HTTP `503` `TOKEN_UNAVAILABLE`
before external I/O. The Gateway sends no `anthropic-beta` header, so beta-only
features are outside this contract.

The Gateway calls REST with Go `net/http`; there is no Anthropic SDK dependency
(SDK retries would violate the single-request contract and the module dependency
set is frozen). The pinned API version is `2023-06-01`, the current Messages API
version. The configured model must support the Messages API and tool use; the
Gateway does not infer capability from model names.

Primary sources checked 2026-09-13:

- [Messages API reference](https://platform.claude.com/docs/en/api/messages)
- [Handling stop reasons](https://platform.claude.com/docs/en/build-with-claude/handling-stop-reasons)
- [Errors](https://platform.claude.com/docs/en/api/errors)
- [Prompt caching and Usage fields](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)
- [Tool use overview](https://platform.claude.com/docs/en/agents-and-tools/tool-use/overview)

### 35.2 Request, Model, Options, and Messages

Model override/default and the single §32 catalog remain unchanged; `model_level`
is consumed locally. The Messages API requires `max_tokens`, which the normalized
Generate request does not carry: when neither the catalog base/level options nor
the request options provide `max_tokens`, the Gateway sends `16000`. A provided
`max_tokens` is forwarded unchanged. Long outputs are bounded by the shared
`LLM_SERVER_PROVIDER_TIMEOUT`, not by this ceiling.

Every `system` message, regardless of position, becomes one entry of the top-level
`system` array (`{"type":"text","text":...}`) in submission order; the array is
omitted when there is none. The Messages API has no in-conversation system role,
so a mid-conversation system message is hoisted to the top.

Remaining messages become content-block turns. Consecutive same-role turns are
merged into one turn because the Messages API alternates `user`/`assistant` and
expects every parallel `tool_result` inside a single `user` turn:

| Normalized message | Wire blocks |
|---|---|
| `user` | `user`: `{"type":"text","text"}` |
| `assistant` | `assistant`: optional `{"type":"text","text"}` when content is non-null, then one `{"type":"tool_use","id","name","input"}` per call (`input` is the normalized `arguments` object) |
| `tool` | `user`: `{"type":"tool_result","tool_use_id","content"}`; a null normalized content omits `content` |

Tool schemas are sent as `{"name","description","input_schema"}` using the §33
wire alias for `name`; Anthropic tool names are `^[a-zA-Z0-9_-]{1,128}$`, of which
the alias table is a subset. Tool call IDs and result `tool_use_id` values are
opaque and preserved exactly.

Accepted `options` keys are:

```text
cache_control, inference_geo, max_tokens, metadata, output_config, service_tier,
stop_sequences, temperature, thinking, tool_choice, top_k, top_p
```

Values are forwarded unchanged and Anthropic validates model-specific
combinations (for example, sampling parameters and `thinking` shapes differ by
model generation). `tool_choice` accepts `{"type":"auto"}`, `{"type":"any"}`,
`{"type":"none"}`, or `{"type":"tool","name":"market.price"}`, each optionally
with `disable_parallel_tool_use`; a named tool must be declared in the same request
and is converted to its §33 alias. `stream`, `system`, `messages`, `model`,
`tools`, `mcp_servers`, `container`, `context_management`, `fallbacks`, `speed`,
`betas`, a string `tool_choice`, an undeclared named tool, and unknown keys return
HTTP `400` `REQUEST_INVALID` before Anthropic I/O.

Anthropic itself rejects, as HTTP `400` mapped to `REQUEST_INVALID`: a first
message that is not `user`, a trailing `assistant` message (assistant prefill is
removed on current models), empty text blocks, and historical call IDs outside
`^[a-zA-Z0-9_-]+$` (IDs produced by another Provider type cannot be replayed to
Anthropic). The Gateway does not pre-validate these.

Known limitation: the normalized contract carries no thinking blocks, so the
Gateway never replays them. Current models accept a thinking-free replay of a
`tool_use` turn (Anthropic's documented strip-and-retry path), but reasoning
continuity across tool rounds is not preserved, and a legacy
`thinking:{"type":"enabled"}` configuration that requires the previous thinking
block may be rejected by Anthropic as `REQUEST_INVALID`.

### 35.3 Response, Stop Reason, and Usage

A success has `type:"message"`, `role:"assistant"`, a non-null `stop_reason`, and
exactly one JSON document. Content blocks are normalized as follows: every `text`
block is concatenated in order into `content` (no text block yields null
`content`); each `tool_use` block becomes a tool call with its opaque `id`, the
alias reversed to the Runtime name (unknown wire-valid names remain unchanged for
Agent unknown-Tool handling), and `input` returned as the arguments object;
`thinking` and `redacted_thinking` blocks are ignored; any other block type is
invalid. Duplicate response IDs and IDs already used by a submitted historical call
are rejected as in §33.2.

| Anthropic `stop_reason` | `finish_reason` | Content rule |
|---|---|---|
| `tool_use` | `tool_call` | at least one `tool_use` block; text may accompany it |
| `end_turn`, `stop_sequence` | `stop` | text or null (Anthropic documents occasional empty `end_turn` responses) |
| `max_tokens`, `model_context_window_exceeded` | `length` | text or null (the turn is truncated) |
| `refusal` | `content_filter` | text or null |
| `pause_turn`, unknown, null | — | HTTP `502` `PROVIDER_RESPONSE_INVALID` |

`tool_use` blocks are valid only with `stop_reason:"tool_use"`; any other stop
reason carrying a `tool_use` block (including a `max_tokens` turn truncated inside
a tool call, which Anthropic says must be retried with a larger `max_tokens`) is
HTTP `502` `PROVIDER_RESPONSE_INVALID`, matching the §33 finish/call mismatch rule.

| Anthropic Usage | Normalized Usage |
|---|---|
| `input_tokens + cache_creation_input_tokens + cache_read_input_tokens` | `input_tokens` |
| `output_tokens` | `output_tokens` |
| `cache_read_input_tokens` | `cached_tokens` |
| (not provided) | `total_tokens: null` |

Anthropic's `input_tokens` counts only the uncached remainder of the prompt
(documented: total prompt size = `input_tokens + cache_creation_input_tokens +
cache_read_input_tokens`). The normalized `input_tokens` is therefore the sum of
the components Anthropic actually reported, so that `cached_tokens` remains the
subset already included in `input_tokens` as §32.4 requires; a missing component
contributes nothing, and the sum is null only when Anthropic reported none of
them. `output_tokens` already includes thinking tokens. `total_tokens` is never
synthesized. Absent Usage is null and counts must be nonnegative.

### 35.4 Errors, Cancellation, and No Retry

Anthropic errors use `{"type":"error","error":{"type","message"}}`.

| Condition | HTTP | Code |
|---|---:|---|
| Missing/disabled/unreadable selected Token | 503 | `TOKEN_UNAVAILABLE` |
| Anthropic `401 authentication_error`, `403 permission_error` | 502 | `AUTHENTICATION_FAILED` |
| Anthropic `429 rate_limit_error` | 429 | `RATE_LIMITED` |
| Anthropic `404 not_found_error` whose message starts with `model:` | 400 | `MODEL_NOT_FOUND` |
| Anthropic `400 invalid_request_error`, `413 request_too_large` | 400 | `REQUEST_INVALID` |
| Connect/header/body deadline | 504 | `PROVIDER_TIMEOUT` |
| Caller cancellation, if writable | 502 | `PROVIDER_UNAVAILABLE` |
| Other `404`, `402`, `500 api_error`, `529 overloaded_error`, other non-2xx, connection failure, or redirect | 502 | `PROVIDER_UNAVAILABLE` |
| Invalid/trailing/oversized success body | 502 | `PROVIDER_RESPONSE_INVALID` |

Anthropic returns the same `not_found_error` with a `model: <id>` message both for
a model that does not exist and for one the organization cannot use, so both are
`MODEL_NOT_FOUND`. Error bodies are not copied to callers; redirects remain
disabled. Local failure makes zero upstream requests, otherwise Generate makes
exactly one using the incoming context. It does not retry, honor `retry-after`,
rotate credentials, execute Tools, or retry Agent semantics.

### 35.5 Verification and Unverified Real Smoke

```powershell
go test ./test -run '^TestAnthropic' -count=10
go test ./test -run '^(TestOpenAI|TestCompatible|TestGenerate|TestTokenLifecycle|TestRuntimeModelCatalog|TestModelCatalog|TestInvalidProviderPatch|TestCreateProvider|TestAdmin|TestToken|TestProvider)' -count=1
go test ./test -count=1
go test . ./cmd/... -run '^$' -count=1
go vet ./...
```

Real smoke requires approved `ANTHROPIC_API_KEY` and `ANTHROPIC_MODEL`, a running
Gateway configured per §28.1, and this Admin/Runtime sequence:

```powershell
$ah = @{ Authorization = "Bearer $env:LLM_SERVER_ADMIN_TOKEN" }
$rh = @{ Authorization = "Bearer $env:LLM_SERVER_RUNTIME_TOKEN" }
$p = @{id="anthropic-smoke";name="Anthropic Smoke";type="anthropic";base_url="https://api.anthropic.com/v1";default_model=$env:ANTHROPIC_MODEL;config=@{}} | ConvertTo-Json
Invoke-RestMethod -Method Post http://127.0.0.1:8090/v1/providers -Headers $ah -ContentType application/json -Body $p
$t = @{name="smoke";token=$env:ANTHROPIC_API_KEY} | ConvertTo-Json
Invoke-RestMethod -Method Post http://127.0.0.1:8090/v1/providers/anthropic-smoke/tokens -Headers $ah -ContentType application/json -Body $t | Out-Null
$g = @{provider="anthropic-smoke";messages=@(@{role="user";content="Reply exactly: smoke-ok"});options=@{max_tokens=64}} | ConvertTo-Json -Depth 6
Invoke-RestMethod -Method Post http://127.0.0.1:8090/v1/generate -Headers $rh -ContentType application/json -Body $g
```

This smoke was **not run**: no approved Anthropic credential or billable model was
supplied. The following remain unverified against the real API and are proven only
by the offline fixture: acceptance of a thinking-free `tool_use` replay on the
configured model, the `16000` default `max_tokens` against each model's output
limit, and the exact `model:` message prefix on `not_found_error`.

## 36. Implemented Ticket 11 Native Gemini Contract

Ticket 11 adds `type: "gemini"` to the existing Provider and Generate paths. It
reuses §31 Token selection, §32 model catalog/levels, §33 Tool aliases and call-ID
history checks, and the shared HTTP transport, timeout, cancellation, response
limit, no-redirect, and no-retry behavior. It adds no SDK, Client, Registry, Agent
state, Tool execution, or credential store.

### 36.1 Protocol, Authentication, and Model Scope

The adapter uses the non-streaming Gemini `generateContent` REST method at the
pinned `v1beta` base URL:

```http
POST {base_url without trailing slash}/models/gemini-2.5-flash-lite:generateContent
Content-Type: application/json
x-goog-api-key: <selected Provider Token>
```

The public-service `base_url` is
`https://generativelanguage.googleapis.com/v1beta`; it is configurable for local
fixtures. The API key is sent only in `x-goog-api-key`, never in the URL or
`Authorization`. `config` remains empty and all credentials use the encrypted
Provider Token API. A missing, disabled, or unreadable selected Token is HTTP `503`
`TOKEN_UNAVAILABLE` before Gemini I/O.

The V1 stateless adapter supports exactly `gemini-2.5-flash-lite`. Other names,
including Gemini 3 models and version-like suffixes, fail closed as HTTP `400`
`MODEL_NOT_FOUND` before Gemini I/O once a usable Provider snapshot is resolved.
This narrow scope is intentional: Google's current thinking guide lists this model
with thinking off by default, while Gemini 3 and other thinking-enabled stateless
turns require opaque thought blocks/signatures to be replayed exactly. The current
normalized `Message`/`ToolCall` contract and Agent Runtime do not carry that state.
The adapter does not silently discard it: a response Part containing
`thoughtSignature`, `thought`, `partMetadata`, or any other unsupported Part shape,
or Usage reporting a positive `thoughtsTokenCount`, makes the whole response HTTP
`502` `PROVIDER_RESPONSE_INVALID`. `thinkingConfig` is not an accepted option.

Supporting thinking-enabled Gemini models requires a separately reviewed normalized
opaque-state field that the Agent Runtime persists and replays. Ticket 11 does not
make that cross-service interface change.

Primary sources checked 2026-09-16:

- [GenerateContent REST reference](https://ai.google.dev/api/generate-content)
- [Function calling](https://ai.google.dev/gemini-api/docs/function-calling)
- [Thinking and thought signatures](https://ai.google.dev/gemini-api/docs/thinking#signatures)
- [Gemini API errors](https://ai.google.dev/gemini-api/docs/api-errors)

### 36.2 Request, Options, and Levels

All normalized `system` messages are hoisted, in order, into text Parts under one
top-level `systemInstruction`. At least one non-system message is required. Remaining
messages map as follows; adjacent wire roles are combined without combining Parts or
calls:

| Normalized message | Gemini `contents` |
|---|---|
| `user` | role `user`, text Part |
| `assistant` | role `model`, optional text Part followed by one `functionCall` Part per normalized call |
| `tool` | role `user`, one `functionResponse` Part |

Historical `functionCall` values preserve the normalized `id` and object
`arguments`; only the function name uses the request-local §33 alias. A Tool result
must have non-null content containing exactly one JSON object. Its referenced
historical call supplies the wire function name, and its normalized `tool_call_id`
is copied to `functionResponse.id`. Thus parallel results, including multiple calls
to the same function, are correlated by distinct opaque IDs rather than name or
position. Invalid/null/non-object result content is HTTP `400` `REQUEST_INVALID`
before Provider I/O.

Tool schemas become one Gemini Tool containing `functionDeclarations`; each
declaration carries aliased `name`, `description`, and `parameters`. The adapter
uses Gemini's default automatic function-calling mode. It does not accept a Tool
choice override.

Accepted `options` are the following exact Gemini REST `generationConfig` field
names:

```text
frequencyPenalty, maxOutputTokens, presencePenalty, seed, stopSequences,
temperature, topK, topP
```

They are placed under `generationConfig`; Gemini validates value types, ranges, and
model-specific combinations. `stream`, `candidateCount`, `responseMimeType`,
`responseSchema`, `thinkingConfig`, `toolConfig`, built-in tools, cached content,
media options, and every unknown key are rejected as HTTP `400` `REQUEST_INVALID`
before I/O. One non-streaming candidate is the fixed normalized response shape.
For a catalog mapping, §32 still consumes `model_level` locally and merges the
mapping's base and selected-level options before this allowlist is applied. The
caller cannot mix direct options with `model_level`.

### 36.3 Response, Calls, Finish Reason, and Usage

A normal generated response contains exactly one candidate. A response with no
candidates is accepted only when `promptFeedback.blockReason` is one of `SAFETY`,
`OTHER`, `BLOCKLIST`, `PROHIBITED_CONTENT`, or `IMAGE_SAFETY`; it returns null
content and `finish_reason: "content_filter"`. When candidate content exists its role
is `model`. A Part must contain exactly one supported data field: string `text` or
`functionCall`. Text Parts concatenate in order. A function call requires a
nonblank Provider-supplied `id`, a wire-valid name, and object `args`. Although the
Gemini REST field is optional, this Gateway does not invent a missing ID because an
invented name/position scheme cannot safely correlate repeated same-name calls.
Duplicate IDs and IDs already present in submitted history invalidate the entire
response. Arguments are returned as a canonical JSON object and aliases are reversed
as in §33.

| Gemini `finishReason` | Normalized result |
|---|---|
| `STOP` with one or more calls | `finish_reason: "tool_call"` |
| `STOP` with text and no calls | `finish_reason: "stop"` |
| `MAX_TOKENS` with no calls | `finish_reason: "length"` |
| `SAFETY`, `RECITATION`, `BLOCKLIST`, `PROHIBITED_CONTENT`, `SPII`, `IMAGE_SAFETY` with no calls | `finish_reason: "content_filter"` |
| No candidate and a recognized `promptFeedback.blockReason` | `finish_reason: "content_filter"`, null content |

Missing/unknown finish reasons, `STOP` without text or calls, calls with any
non-`STOP` reason, `MALFORMED_FUNCTION_CALL`, `UNEXPECTED_TOOL_CALL`, missing
candidates without a recognized prompt block, a prompt block alongside a candidate,
content required by the selected finish rule, malformed/trailing JSON, or unsupported
Parts are HTTP `502` `PROVIDER_RESPONSE_INVALID`.

| Gemini `usageMetadata` | Normalized Usage |
|---|---|
| `promptTokenCount` | `input_tokens` |
| `candidatesTokenCount` | `output_tokens` |
| `cachedContentTokenCount` | `cached_tokens` |
| `totalTokenCount` | `total_tokens` |

Absent `usageMetadata` is `usage:null`. A present object always returns all four
normalized nullable members; an absent Gemini member stays null. Counts must be
nonnegative and no missing value is calculated. Positive `thoughtsTokenCount` is
the unsupported-state failure described in §36.1, not silently omitted.

### 36.4 Errors, Cancellation, and Verification

| Condition | HTTP | Code |
|---|---:|---|
| Unsupported local model | 400 | `MODEL_NOT_FOUND` |
| Gemini `404` | 400 | `MODEL_NOT_FOUND` |
| Gemini `400` | 400 | `REQUEST_INVALID` |
| Gemini `401`/`403` | 502 | `AUTHENTICATION_FAILED` |
| Gemini `429` | 429 | `RATE_LIMITED` |
| Connect/header/body deadline | 504 | `PROVIDER_TIMEOUT` |
| Caller cancellation, if writable | 502 | `PROVIDER_UNAVAILABLE` |
| Other non-2xx, connection failure, or redirect | 502 | `PROVIDER_UNAVAILABLE` |
| Invalid/unsupported success response | 502 | `PROVIDER_RESPONSE_INVALID` |

Provider error prose is not returned. Local validation makes zero upstream calls;
otherwise Generate makes exactly one request using its request-local Provider/Token
snapshot and incoming context. It does not retry, follow redirects, honor retry
headers, rotate credentials, execute Tools, or make an Agent decision.

Offline verification from `llm_provider_server` uses only temporary SQLite files and
local `httptest` credentials/endpoints:

```powershell
go test ./test -run '^TestGemini' -count=1
go test . ./cmd/... -run '^$' -count=1
go vet ./...
```

No live request was run and no Gemini credential was read from or written to an
`.env` file. A future real smoke requires separate approval, a process-environment
`GEMINI_API_KEY`, access to exact model `gemini-2.5-flash-lite`, and the public
`https://generativelanguage.googleapis.com/v1beta` base URL. Offline fixtures prove
the documented wire contract but are not claimed as real-provider, quota, billing,
or model-availability acceptance.
## 37. Claude Code conversation transport sessions and continuation

`POST /v1/generate` accepts an optional `conversation_id` of 1–128 characters with no control characters. Non-Claude providers ignore it. For `claude_code`, the Gateway retains one isolated CLI transport session per conversation and provider/model/effort/tool/configuration/credential signature. Local-login identity is derived from the same bounded, validated document snapshot copied into the temporary session directory; changing that source starts a new session without changing an invocation already in flight. The first call uses a new UUID with `--session-id`; later calls use `--resume` and send only the exact new message suffix. A changed message prefix, shorter history, changed signature, missing work directory, process restart, or idle expiry starts a fresh CLI session. A failed resume is discarded and retried cold once unless the request was canceled.

An identical request has no new message suffix while the CLI session already contains hidden assistant output. The Gateway therefore executes it cold with a fresh UUID instead of issuing an empty resume or providing an implicit response cache.

Idle sessions expire after `LLM_SERVER_HARNESS_SESSION_TTL` (default `30m`); collection runs at least once per minute and never removes active sessions. Shutdown rejects admission, cancels and joins active CLI work, then removes only Gateway-created temporary session directories. Run completion does not immediately notify the Gateway, so TTL is the normal cleanup boundary.

Claude responses may include nullable `num_turns` and `total_cost_usd`; present values must be nonnegative and cost must be finite. Input-token and cached-token diagnostics remain provider-reported metrics. Session reuse and reported cache metrics do not guarantee cost, latency, or token savings, and restart/expiry deliberately falls back to a full prompt.

### 37.1 Exact optional continuation

The Runtime may retain complete normalized `messages` while also sending:

```json
{
  "continuation": {
    "after_messages": 2,
    "messages": [
      {"role":"tool","content":"{\"ok\":true}","tool_call_id":"call-price"}
    ]
  }
}
```

`after_messages` is the positive number of messages in the last request that the
Runtime considers successfully acknowledged. The full `messages` array remains
authoritative for validation and cold recovery. For a managed Claude session, the
Gateway uses the explicit delta only when all of these are exact:

1. `after_messages` equals the retained successful request length;
2. that retained request is the prefix of the current full history;
3. the next full-history message equals the prior normalized assistant output,
   including tool IDs, names and canonical object arguments; and
4. every remaining full-history message equals `continuation.messages`, which is
   nonempty.

The assistant output is deliberately absent from the delta because it already
exists inside the resumed CLI session. An empty, changed, missing, or otherwise
unprovable continuation never causes an empty resume. It runs one cold invocation
with complete `messages`. A resume process failure still has only the existing one
bounded cold fallback. Callers that omit `continuation` retain the previous exact
prefix/suffix behavior. Codex and REST/native providers ignore continuation and
always receive complete `messages`.

`continuation.after_messages <= 0` is HTTP `400` `REQUEST_INVALID`. Other
continuation/state mismatches are transport cache misses rather than malformed
Agent histories, because the independently validated full history is sufficient
for safe cold execution.

### 37.2 Optional diagnostics and safe operational events

Managed Claude successes add:

```json
{
  "session_mode": "resume",
  "invocation_count": 1
}
```

`session_mode` is `cold` or `resume`. `fallback_reason` is omitted without a
fallback and otherwise is one of the fixed safe categories
`continuation_unavailable`, `continuation_invalid`,
`session_directory_missing`, `history_prefix_changed`, `no_new_messages`, or
`resume_failed`. `invocation_count` is one, except a failed resume followed by the
single successful cold retry reports two. These fields are optional for consumers
and omitted by stateless/native and Codex responses.

The terminal emits only fixed harness type/event/category values at actual
fallback and error points. It never logs prompts, request messages, credentials,
authentication documents, stdout, stderr, or arbitrary CLI error prose.

### 37.3 Offline regression evidence and limits

The deterministic fixture workload sends the same four-message full history by
both paths. The legacy suffix prompt is exactly 287 bytes and contains two
messages, including one duplicated assistant message. The explicit continuation
prompt is exactly 177 bytes, contains only the one tool-result delta, performs no
extra cold invocation, and contains zero assistant messages. The test also proves
that invalid counts, changed prior output, changed/empty deltas, and absent retained
state use one full cold invocation. The unprovable-continuation cold prompt is
exactly 764 bytes, equal to the same full request with no continuation.

This is payload regression evidence only. It is not proof of live token billing,
cache, latency, model availability, Web Search/Fetch, or cost savings. No live CLI
or credential was used for this amendment.

### 37.4 Codex ticket-12 consistency status

Offline process fixtures now compare the complete ordered Codex 0.145.0 argument
vector, isolated environment allowlist, exact structured-output schema, API-key
and copied-local-login modes, model/default-model behavior, Web Search setting,
every disabled feature, process-tree cancellation, bounded output, JSONL Usage and
HTTP error mapping. Codex remains ephemeral and receives full context; no resume
scope was added.

The original ticket-12 live gaps remain: this amendment made no real Provider
call and did not verify an approved API credential, actual model entitlement,
live Web Search/Fetch, live structured domain-tool return, malicious live config
override rejection, quota/billing behavior, or current vendor capability drift.
The 2026-09-13 observations in `test/harness-validation.md` remain historical
evidence, not current live acceptance.

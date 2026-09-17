# Live model catalog administration

User-requested extension to the environment-only V1 catalog. Admin authorization
is required; Runtime credentials receive 401 `AUTHENTICATION_FAILED`.

## Endpoints

- `POST /v1/model-catalog`: create, 201. Duplicate name: 409 `MODEL_ALREADY_EXISTS`.
- `PUT /v1/model-catalog/{model_name}`: replace the mapping, 200. Name is immutable
  and must match the body. Missing name: 404 `MODEL_NOT_FOUND`.
- `DELETE /v1/model-catalog/{model_name}`: 204, empty response. Missing: 404.
- `GET /v1/models`: existing Runtime-authenticated list, unchanged response shape.

Create/replace body:

```json
{
  "model_name": "my-chat",
  "provider_id": "my-provider",
  "model": "upstream-model-id",
  "options": {},
  "levels": {"high": {"reasoning_effort": "high"}},
  "enabled": true
}
```

`model_name`, `provider_id`, `model` are required. Existing catalog name/level/
reserved-option validation applies. Legal levels: low, medium, high, max; actual
options depend on the provider. Omitted enabled defaults true; null is invalid.

CLI Harness Providers (`codex`, `claude_code`) accept exactly one option,
`reasoning_effort`, in `options` and in each level; any other key is 400
`REQUEST_INVALID` at save time. Each CLI has its own scale, validated here
because neither CLI rejects an unknown value: Codex takes minimal, low, medium,
high, xhigh (sent as `-c model_reasoning_effort=...`), Claude Code takes low,
medium, high, xhigh, max (sent as `--effort`). Everything else about the
invocation stays Server-controlled.
Omitted/null options and levels normalize to empty objects. No query parameters.
Unknown body fields, null root, invalid/ambiguous mapping: 400 `REQUEST_INVALID`.
Missing Provider: 404 `PROVIDER_NOT_FOUND`. Storage failure: 500 `INTERNAL_ERROR`;
the active catalog is unchanged. Other methods: 405 with `Allow`.

Success returns the normalized mapping with `available` (enabled mapping and
Provider), not confirmation of upstream credential/model availability. Errors use
`{"error":{"code":"...","message":"..."}}`.

## Persistence and update semantics

The environment catalog bootstraps a database without saved Admin changes. The
first successful Admin mutation stores the whole catalog in SQLite. From then on,
that saved catalog is authoritative, including an empty catalog; restarting with
an old environment cannot restore deleted entries. Keep bootstrap configuration
syntactically valid since startup still validates it before opening storage.

Updates are serialized, persisted before publication, and visible to subsequent
catalog lookups and Generate calls without restart. In-flight Generate retains its
already-resolved request options. Deleting a Provider leaves its model mappings
unavailable so the administrator can explicitly reassign or remove them.

Local verification (Go, no external credentials; temporary DB and HTTP fixture):

```powershell
go test ./test -run '^TestModelManagement' -count=1
```

Tests create a model, use it immediately, reopen the DB, reject invalid changes,
and verify deletion persists. Fixtures and DBs are cleaned by the test runner.

# Stock AI — compiled Windows x64 distribution

Requirements: Windows x64 and PowerShell 7. Go, Node and npm are not needed to run
this folder. Start with `pwsh -File ./start.ps1` from this folder or its full path.

Open **http://127.0.0.1:8088** and sign in with `USER_TOKEN` from `.env`.
The release frontend does not contain a prefilled login token. On the first
successful package only, `.env` is generated from the configuration selected by
the build command; it is not public data. Later packages never read, append, or
replace the existing `.env`.

## Files

- `backend.exe`: trading, Accounts, market and messages.
- `llm-provider-server.exe`: Provider/Token management and model catalog.
- `agent-server.exe`: Agent Runtime, Events, History, Memory and monitoring.
- `console.exe`: static frontend and authenticated same-origin reverse proxy.
- `public/`: frontend assets only; `.env` and `data/` cannot be served here.
- `.env`: all service configuration, including the existing encryption key.
- `data/`, `logs/`: created on first start; preserved on subsequent builds.

## Rebuilding this folder

Stop every process running executables from this distribution, then run the build.
The build does not stop services automatically. A locked release artifact fails
preflight without changing the installed release.

All binaries, frontend files, scripts, documentation, and explicitly requested
CLI tools are prepared and validated before publication. Publication keeps a
temporary backup and restores the prior artifact set if replacement fails. A
failed build does not modify `.env`, databases, Agent History, or logs. Those
persistent files are also preserved byte-for-byte after a successful rebuild.

`-SkipFrontendBuild` requires an existing non-empty `frontend/dist`. CLI tools
are optional: request them with `-CodexExecutable <path-to-codex.exe>` and/or
`-ClaudeExecutable <path-to-claude.exe>`. An explicitly requested missing CLI is
an error; omitting an option leaves that installed tool directory unchanged.
When creating the distribution for the first time, use `-ConfigurationFile` to
select the launch configuration used only to generate the initial `.env`.

This distribution uses separate empty databases on first start so it cannot race
with the already running development services. To migrate existing data, stop all
services using the original DBs, then copy all three databases and the Agent
History directory to their corresponding `data/` paths. Keep the Gateway master
key unchanged. Do not copy live SQLite files or run two Agent servers on one DB.

## Internet / LAN hosting

Internal services bind loopback ports 17794, 18090 and 18080. Expose only the
console: place HTTPS termination in front of port 8088, or configure
`CONSOLE_TLS_CERT` and `CONSOLE_TLS_KEY` and set `CONSOLE_ADDR=0.0.0.0:8088`.
Use strong USER/admin/runtime tokens before public access. Update matching Agent
dependency-token settings together. Do not change an existing database's master
key to rotate an admin token.

The console authenticates browser USER credentials before adding internal service
credentials. The browser never receives Agent/Gateway admin/runtime tokens.
Backend account-level requests retain their own authorization rules.

Provider → Token → **新增模型** → Account → Session → start is the shortest test
path. Native OpenAI, Anthropic and compatible providers are wired. Codex and
Claude Code run as optional CLI Harness providers. When explicitly requested,
the build copies them into `tools/`; a newly generated `.env` points the matching
executable setting there and references the current user's CLI authentication
file without reading or copying that file. Existing `.env` settings remain the
operator's responsibility. Harness status shows on the Providers page; a Harness
model accepts no options or levels. Gemini and live-provider acceptance remain
outstanding. The package is a deployable integration build, not final full-V1
acceptance approval.

Stop Agent first with Ctrl+C in its window, then Gateway and Backend. Keep the
folder's `.env`, databases and History for the next start. Close console last.

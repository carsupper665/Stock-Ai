# Local integrated console

Start from the repository root with PowerShell 7:

```powershell
./scripts/start-local.ps1
```

Uses existing `.claude/launch.json` local configuration without evaluating its
shell commands. Preserves databases and the Gateway encryption key. Existing
listeners are left running; stop an old process before rebuilding it.
Logs/binaries go to `%TEMP%/stock-ai-local`. Local development credentials must
not be used for a public deployment.

Open **http://127.0.0.1:5173**. Login uses the existing `.env.local` dev USER token.

1. Providers: create/select a Provider and configure its API Token.
2. Model catalog: click **新增模型**, choose that Provider, supply a logical name
   and real upstream model ID. Options/levels are JSON objects. Saving takes
   effect immediately and persists through Gateway restart.
3. Create a trading Account, then a Session using the new model. Start it.
4. Session overview: account-wide state, active/pending events and Run state.
5. Runs: load/download the fixed 20-Run History shard on demand.
6. Memory: metadata list, lazy detail, mark expired (audit content preserved).
7. Monitoring: server Usage and Tool timing statistics, account-wide Ledger and
   Positions. Source links navigate to the correct Session/Run; unattributed
   account changes remain visible.

Missing upstream Usage stays unknown. Backend failures are shown instead of
invented values. Available means the mapping and Provider are enabled; upstream
authentication/model access still requires a valid API Token and model ID.

This is the integrated local manual-test checkpoint. Full V1 acceptance and final
consolidated review remain pending, including previously documented Gemini/Harness
and live-provider acceptance gaps.

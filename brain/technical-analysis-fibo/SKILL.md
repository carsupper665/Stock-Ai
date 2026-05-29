# Technical Analysis + FIBO Skill

Use this skill when you need to run technical-analysis workflows from backend APIs and derive Fibonacci (FIBO) levels for decision support.

This skill is analysis-focused. It does not include autonomous trading loops.

## Scope

- Call admin technical-analysis APIs correctly.
- Parse kline and indicator payloads.
- Compute FIBO retracement/extension levels.
- Build a repeatable analysis checklist (trend, pullback zones, invalidation, targets).

## Out of Scope

- Replay admin controls (`seek/speed/pause/resume`).
- Signal auto-generation bots.
- Mainnet/testnet key management.

## API Auth (Admin Session)

Technical-analysis APIs in this project are admin endpoints and use session cookie auth.

```powershell
$Base = "http://127.0.0.1:7794"
$Session = New-Object Microsoft.PowerShell.Commands.WebRequestSession

Invoke-RestMethod -Method Post -WebSession $Session -Uri "$Base/admin/login" -ContentType "application/json" -Body '{"username":"admin","password":"123"}'
```

## Technical Analysis API Contracts

### 1) Latest Price

- `GET /admin/live/price?symbol=BTCUSDT`

Response core fields:

- `symbol`
- `last_price`
- `state` (`active|stale|degraded`)
- `freshness_ms`

### 2) Klines

- `GET /admin/live/klines?symbol=BTCUSDT&interval=1h`

Response shape:

```json
{
  "items": [
    {
      "symbol": "BTCUSDT",
      "at": "2026-05-28T16:00:00Z",
      "open": 72000,
      "high": 72500,
      "low": 71800,
      "close": 72400,
      "volume": 1234.56
    }
  ]
}
```

### 3) Indicators

- `GET /admin/live/indicators?symbol=BTCUSDT&interval=1h`

Response shape:

```json
{
  "symbol": "BTCUSDT",
  "interval": "1h",
  "indicators": {
    "sma_20": [{"at":"...","time":"...","value":123}],
    "macd_line": [{"at":"...","time":"...","value":0.1}]
  }
}
```

Indicator keys currently provided:

- `sma_20`
- `macd_line`, `macd_signal`, `macd_hist`
- `rsi_14`
- `bb_upper_20_2`, `bb_middle_20`, `bb_lower_20_2`
- `obv`
- `kd_k`, `kd_d`, `kd_j`
- `dmi_plus_di`, `dmi_minus_di`, `dmi_adx`
- `ad`
- `bias_20`

## Minimal API Runbook (PowerShell)

```powershell
$Symbol = "BTCUSDT"
$Interval = "1h"

$Price = Invoke-RestMethod -WebSession $Session -Uri "$Base/admin/live/price?symbol=$Symbol"
$K = Invoke-RestMethod -WebSession $Session -Uri "$Base/admin/live/klines?symbol=$Symbol&interval=$Interval"
$I = Invoke-RestMethod -WebSession $Session -Uri "$Base/admin/live/indicators?symbol=$Symbol&interval=$Interval"

"Price: $($Price.last_price) | Klines: $($K.items.Count) | Indicators: $($I.indicators.Keys.Count)"
```

## FIBO Strategy Workflow

### Step 1: Define Swing Range

Choose analysis window (example: last 120 candles in `1h`).

- `H` = swing high (max `high` in window)
- `L` = swing low (min `low` in window)
- `R = H - L`

### Step 2: Compute Retracement Levels

For **uptrend pullback** (from `L -> H`):

- `0.236 = H - R*0.236`
- `0.382 = H - R*0.382`
- `0.5   = H - R*0.5`
- `0.618 = H - R*0.618`
- `0.786 = H - R*0.786`

For **downtrend rebound** (from `H -> L`):

- `0.236 = L + R*0.236`
- `0.382 = L + R*0.382`
- `0.5   = L + R*0.5`
- `0.618 = L + R*0.618`
- `0.786 = L + R*0.786`

### Step 3: Compute Extension Targets

Uptrend breakout targets:

- `1.272 = H + R*0.272`
- `1.618 = H + R*0.618`

Downtrend continuation targets:

- `1.272 = L - R*0.272`
- `1.618 = L - R*0.618`

### Step 4: Confluence Check (must-have)

Only treat FIBO zone as actionable when at least 2 confluences exist:

- FIBO level near `sma_20` or `bb_middle_20`
- RSI regime confirmation (`rsi_14` not diverging hard against setup)
- MACD momentum alignment (`macd_line` vs `macd_signal`)
- DMI trend strength (`dmi_adx` + DI direction)

### Step 5: Risk Envelope

- Entry: near selected retracement zone (ex: 0.5~0.618)
- Invalidation: below/above nearest structural swing (not only exact FIBO line)
- TP1/TP2: extension levels (1.272 / 1.618)

## Quick FIBO Calculator (PowerShell)

```powershell
$Candles = $K.items | Select-Object -Last 120
$H = ($Candles | Measure-Object high -Maximum).Maximum
$L = ($Candles | Measure-Object low -Minimum).Minimum
$R = $H - $L

$FiboUp = [ordered]@{
  "0.236" = $H - $R * 0.236
  "0.382" = $H - $R * 0.382
  "0.500" = $H - $R * 0.500
  "0.618" = $H - $R * 0.618
  "0.786" = $H - $R * 0.786
  "1.272" = $H + $R * 0.272
  "1.618" = $H + $R * 0.618
}

$FiboUp
```

## Pass Criteria

Analysis run is valid only when:

1. Admin login/session works.
2. `/admin/live/klines` returns non-empty candles for chosen symbol/interval.
3. `/admin/live/indicators` returns indicator map with timestamped points.
4. FIBO levels are computed from explicit swing `H/L` window.
5. Final conclusion includes confluence + invalidation + target levels.

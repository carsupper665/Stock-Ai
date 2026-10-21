---
name: trade-plan
description: Turn a trade idea into exact numbers (stop, target, quantity, max loss, margin) before opening or adding to any position. Read it before your first place_order of a Run.
---

# trade-plan

plan_trade does the arithmetic of a trade so you never estimate it. It reads the bound Account and the symbol's rules and returns the plan. It does not judge whether the trade is good: that decision stays yours.

## Steps

1. Decide the side, the order type and the stop from your own analysis. The stop is the price that proves the idea wrong.
2. Call plan_trade with symbol, side, stop_loss, either take_profit or target_r, and either risk_pct (the percent of equity you accept losing at the stop) or quantity. A market order plans at the current price; a limit order plans at its price.
3. If problems is not empty there is no order. Change the inputs and call plan_trade again.
4. Call place_order with plan_trade's order object exactly as returned. A different entry, stop, target or size is a new plan_trade call first.
5. When you explain the trade, quote plan_trade's numbers: risk_per_unit, r_multiple, quantity, max_loss, max_loss_pct_of_equity, margin_required. Never compute or round them yourself.

## Notes

- Prices are rounded to the symbol's tick size and the quantity is rounded down to its step size, so max_loss can be a little below what you asked for.
- Fees are not included, and in a fast move a stop fills at the market price, so a real loss can exceed max_loss.
- The plan is for the moment it was made. If the price has moved by the time you act, plan again.

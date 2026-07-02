---
name: spark-research
description: >
  Systematic framework for researching, verifying, backtesting, and vetting non-machine-learning stock trading strategies for retail execution. Ensures strategies remain current and effective in the 2024–2026+ regime.
---

# Spark-Research: Non-ML Stock Trading Strategy Verification & Vetting Pipeline

Use this skill to research, backtest, verify, and design production-ready setups for rule-based, deterministic stock trading strategies that do not rely on machine learning.

## Phase 1: Strategy Specification (Deterministic Rules)
1. **No Machine Learning:** The strategy must rely entirely on mathematical, logical, and structural parameters (e.g., technical indicator thresholds, cointegration spreads, corporate actions, sentiment extremes).
2. **Deterministic Entry/Exit:** Specify exact triggers:
   - Setup criteria (e.g., trend filters, volume nodes, cointegration residuals).
   - Entry triggers (e.g., Z-score crossovers, candle completions).
   - Exits (target exits, stop-loss heuristics, time-based cutoffs).

## Phase 2: Retail Viability Assessment
1. **Broker APIs:** Evaluate compatibility with retail broker APIs (Alpaca, Interactive Brokers, Tradier).
2. **Capital Constraints:** Design for accounts under $100k-$1M.
3. **Margin & Day Trading Checks:**
   - Under $100k: Restrict leverage calculations to standard Reg T margin (2:1).
   - Over $100k-$125k: Incorporate Portfolio Margin (TIMS risk-based offsets, up to 6:1+ leverage).
   - Watermark Safety: Require a hard-stop deleveraging trigger at $110k equity to exit positions before the broker downgrades the account to Reg T margin at $100k.
   - Leverage June 2026 FINRA day trading margin rules (removal of the $25k PDT limit, replacement with broker-managed real-time intraday margin checks).
4. **Borrow Fee Analysis:** For short-side strategies, verify borrow availability and integrate borrowing fee rates into the cost model.
5. **Latency Verification:**
   - Flag any strategy with expected holding periods under 5 minutes (such as Order Book Imbalance or Level 2 order flow) as non-viable due to retail API network latency (>50ms RTT).

## Phase 3: Transaction Cost Analysis (TCA) Modeling
1. **Implementation Shortfall (IS):**
   $$\text{IS} = (\text{Decision Price} - \text{Execution Price}) \times \text{Volume} + \text{Fees}$$
2. **Slippage Modeling:** Estimate slippage based on trade size relative to Average Daily Volume (ADV) and realized daily volatility:
   $$\text{Slippage (bps)} = \gamma \times \left( \frac{\text{Order Size}}{\text{ADV}} \right)^{\alpha} \times \sigma_{\text{daily}} + Spread_{\text{half}}$$
3. **Friction Filter:** If total transaction cost (slippage + commission + borrow fees) exceeds 30% of the strategy's average historical return, flag the strategy as non-viable.

## Phase 4: Volatility & Risk Management Sizing
1. **Sizing Engine:**
   - For alpha-driven trading: Use Fractional Kelly sizing (e.g., quarter-Kelly) adjusted for win rate ($p$) and payoff ratio ($b$).
   - For multi-asset baskets: Use Risk Parity (volatility-inverse weighting) to ensure equal risk contribution.
2. **Exits:** Implement volatility-adjusted exits (e.g., Chandelier Exit using $22$-period ATR with multipliers dynamically scaled between 2.0 and 4.5 based on 252-day relative beta) or hard time-based exits.

## Phase 5: Production Infrastructure Blueprint
1. **Hosting:** Require Virtual Private Server (VPS) hosting co-located near broker execution points-of-presence (e.g., NY4).
2. **Asynchronous Execution:** Decouple signal generation from order routing using message queuing (RabbitMQ, NATS, Kafka).
3. **Failover Execution Queue:** Specify fallback behaviors for API timeout, rate limits, or broker server outages.

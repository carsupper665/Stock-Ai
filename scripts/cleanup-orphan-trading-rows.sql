-- 清掉帳號已刪除但留下來的交易狀態。
--
-- 在帳號刪除改為連帶清理之前，DELETE /v1/accounts/{id} 只刪 accounts 那一列，
-- 部位與掛單會留下變成孤兒：撮合引擎每輪仍會掃到它們，價格穿過停損停利時嘗試平倉
-- 卻找不到帳號，於是每秒寫一次「平倉失敗: 資料不存在」，而且沒有終止狀態。
--
-- 執行前先停掉 backend（SQLite 是單檔 live DB），跑完再啟動。
-- sqlite:   sqlite3 dist/data/backend.db < scripts/cleanup-orphan-trading-rows.sql
-- postgres: psql "$DB_DSN" -f scripts/cleanup-orphan-trading-rows.sql

-- 先看要刪什麼。
SELECT 'orphan position' AS kind, p.id, p.account_id, p.symbol, p.side, p.quantity
FROM positions p LEFT JOIN accounts a ON a.id = p.account_id WHERE a.id IS NULL
UNION ALL
SELECT 'orphan open order', o.id, o.account_id, o.symbol, o.side, o.quantity
FROM orders o LEFT JOIN accounts a ON a.id = o.account_id
WHERE a.id IS NULL AND o.status = 'open';

DELETE FROM positions WHERE account_id NOT IN (SELECT id FROM accounts);
DELETE FROM orders WHERE status = 'open' AND account_id NOT IN (SELECT id FROM accounts);

-- 成交紀錄與 Ledger 刻意保留：它們是審計紀錄，也不會被撮合引擎掃到。

package history

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/newo-ether/filo/internal/protocol"
)

const receiptCandidateLimit = 128
const receiptBodyBytes = scanBytes

// Select keys before inspecting JSON, including before any sort or filter.
// octet_length reads the stored length without loading oversized TEXT values;
// CASE never fetches those bodies. Missing evidence remains unconfirmed.
func indexedInputReceipts(ctx context.Context, tx *sql.Tx, id string) ([]protocol.InputReceipt, error) {
	rows, err := tx.QueryContext(ctx, `SELECT item_id,turn_id FROM thread_items
		WHERE thread_id=? ORDER BY rollout_ordinal DESC,item_id DESC LIMIT ?`, id, receiptCandidateLimit)
	if err != nil {
		return nil, err
	}
	var candidates []protocol.InputReceipt
	for rows.Next() {
		var receipt protocol.InputReceipt
		if err := rows.Scan(&receipt.MessageID, &receipt.TurnID); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, receipt)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var receipts []protocol.InputReceipt
	for _, receipt := range candidates {
		var body sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT CASE WHEN octet_length(item_json)<=? THEN item_json END
			FROM thread_items WHERE thread_id=? AND turn_id=? AND item_id=?`,
			receiptBodyBytes, id, receipt.TurnID, receipt.MessageID).Scan(&body)
		if err != nil {
			return nil, err
		}
		var item struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			ClientID string `json:"clientId"`
		}
		if !body.Valid || json.Unmarshal([]byte(body.String), &item) != nil || item.Type != "userMessage" || item.ID != receipt.MessageID {
			continue
		}
		receipt.ClientID = item.ClientID
		receipts = append(receipts, receipt)
	}
	for left, right := 0, len(receipts)-1; left < right; left, right = left+1, right-1 {
		receipts[left], receipts[right] = receipts[right], receipts[left]
	}
	return protocol.MergeInputReceipts(receipts), nil
}

package codex

import (
	"slices"

	"github.com/newo-ether/filo/internal/nativejson"
	"github.com/newo-ether/filo/internal/protocol"
)

// desktopInputReceipts reads accepted native identities before visible-turn
// truncation. Only the newest bounded client identities are retained.
func desktopInputReceipts(turns []any) []protocol.InputReceipt {
	var receipts []protocol.InputReceipt
	seen := make(map[string]protocol.InputReceipt)
	conflicts := make(map[string]bool)
	for turnIndex := len(turns) - 1; turnIndex >= 0; turnIndex-- {
		turn, _ := asMap(turns[turnIndex])
		items, _ := turn["items"].([]any)
		for itemIndex := len(items) - 1; itemIndex >= 0; itemIndex-- {
			item, _ := asMap(items[itemIndex])
			receipt := protocol.InputReceipt{
				TurnID:    nativejson.Text(turn["turnId"]),
				MessageID: nativejson.Text(item["id"]),
				ClientID:  nativejson.Text(item["clientId"]),
			}
			switch nativejson.Text(item["type"]) {
			case "userMessage":
			case "steeringUserMessage":
				if nativejson.Text(item["status"]) != "accepted" {
					continue
				}
				receipt.ClientID = nativejson.Text(item["clientUserMessageId"])
				if serverID, present := item["serverUserMessageId"]; present && serverID != nil {
					receipt.MessageID = nativejson.Text(serverID)
				}
			default:
				continue
			}
			if !receipt.Valid() {
				continue
			}
			if previous, exists := seen[receipt.ClientID]; exists {
				if previous != receipt {
					conflicts[receipt.ClientID] = true
				}
				continue
			}
			if len(receipts) == protocol.MaxInputReceipts {
				continue
			}
			seen[receipt.ClientID] = receipt
			receipts = append(receipts, receipt)
		}
	}
	kept := receipts[:0]
	for _, receipt := range receipts {
		if !conflicts[receipt.ClientID] {
			kept = append(kept, receipt)
		}
	}
	slices.Reverse(kept)
	return kept
}

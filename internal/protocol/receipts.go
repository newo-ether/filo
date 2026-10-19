package protocol

import "strings"

const MaxInputReceipts = 128

// MergeInputReceipts combines native evidence independently of page contents.
// Conflicting identities for one client are omitted rather than confirmed.
func MergeInputReceipts(groups ...[]InputReceipt) []InputReceipt {
	var result []InputReceipt
	seen := make(map[string]InputReceipt)
	conflicts := make(map[string]bool)
	for _, group := range groups {
		for _, receipt := range group {
			if !receipt.Valid() || conflicts[receipt.ClientID] {
				continue
			}
			if previous, exists := seen[receipt.ClientID]; exists {
				if previous != receipt {
					conflicts[receipt.ClientID] = true
				}
				continue
			}
			seen[receipt.ClientID] = receipt
			result = append(result, receipt)
		}
	}
	kept := result[:0]
	for _, receipt := range result {
		if !conflicts[receipt.ClientID] {
			kept = append(kept, receipt)
		}
	}
	if len(kept) > MaxInputReceipts {
		kept = kept[len(kept)-MaxInputReceipts:]
	}
	return kept
}

func (receipt InputReceipt) Valid() bool {
	for _, value := range []string{receipt.ClientID, receipt.TurnID, receipt.MessageID} {
		if strings.TrimSpace(value) == "" || len(value) > 1024 {
			return false
		}
	}
	return true
}

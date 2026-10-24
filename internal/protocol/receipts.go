package protocol

import "strings"

const MaxInputReceipts = 128

// MergeInputReceipts combines evidence independently of page contents. Conflicting
// native identities are omitted; non-exact receipt progress retains its label.
func MergeInputReceipts(groups ...[]InputReceipt) []InputReceipt {
	var order []string
	seen := make(map[string]InputReceipt)
	conflicts := make(map[string]bool)
	for _, group := range groups {
		for _, receipt := range group {
			if !receipt.Valid() || conflicts[receipt.ClientID] {
				continue
			}
			if previous, exists := seen[receipt.ClientID]; exists {
				merged, ok := mergeReceipt(previous, receipt)
				if !ok {
					conflicts[receipt.ClientID] = true
				} else {
					seen[receipt.ClientID] = merged
				}
				continue
			}
			seen[receipt.ClientID] = receipt
			order = append(order, receipt.ClientID)
		}
	}
	result := make([]InputReceipt, 0, len(order))
	for _, clientID := range order {
		if !conflicts[clientID] {
			result = append(result, seen[clientID])
		}
	}
	if len(result) > MaxInputReceipts {
		result = result[len(result)-MaxInputReceipts:]
	}
	return result
}

func mergeReceipt(previous, next InputReceipt) (InputReceipt, bool) {
	if previous == next {
		return previous, true
	}
	if previous.Kind != "submitted_observed" || next.Kind != previous.Kind ||
		previous.TurnID != "" && next.TurnID != "" && previous.TurnID != next.TurnID ||
		previous.MessageID != "" && next.MessageID != "" && previous.MessageID != next.MessageID {
		return InputReceipt{}, false
	}
	if previous.State == "rejected" || next.State == "rejected" {
		if previous.State != "unconfirmed" && next.State != "unconfirmed" {
			return InputReceipt{}, false
		}
		if next.State == "rejected" {
			return next, true
		}
		return previous, true
	}
	if previous.State == "observed" || next.State == "observed" {
		if next.State == "observed" {
			return next, true
		}
		return previous, true
	}
	if previous.State == "unconfirmed" || next.State == "unconfirmed" {
		next.State = "unconfirmed"
	}
	return next, true
}

func (receipt InputReceipt) Valid() bool {
	for _, value := range []string{receipt.ClientID, receipt.TurnID, receipt.MessageID} {
		if len(value) > 1024 || value != "" && strings.TrimSpace(value) == "" {
			return false
		}
	}
	if strings.TrimSpace(receipt.ClientID) == "" {
		return false
	}
	if receipt.Kind == "submitted_observed" {
		return receipt.State == "submitted" || receipt.State == "unconfirmed" || receipt.State == "rejected" ||
			receipt.State == "observed" && (receipt.MessageID != "" || receipt.TurnID != "")
	}
	return receipt.Kind == "" && receipt.State == "" && receipt.TurnID != "" && receipt.MessageID != ""
}

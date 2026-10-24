package protocol

import "testing"

func TestNonExactReceiptProgressPreservesKindAndNativeConflicts(t *testing.T) {
	base := InputReceipt{ClientID: "client", Kind: "submitted_observed", State: "submitted"}
	observed := base
	observed.State = "observed"
	observed.MessageID = "native-row"
	unknown := base
	unknown.State = "unconfirmed"
	for _, group := range [][]InputReceipt{{base, observed}, {observed, base}, {unknown, observed}, {observed, unknown}} {
		result := MergeInputReceipts(group)
		if len(result) != 1 || result[0] != observed {
			t.Fatalf("lost observation: %+v", result)
		}
	}
	conflict := observed
	conflict.MessageID = "different-row"
	if len(MergeInputReceipts([]InputReceipt{observed, conflict})) != 0 {
		t.Fatal("conflicting native row confirmed")
	}
	exact := InputReceipt{ClientID: "client", TurnID: "turn", MessageID: "row"}
	if len(MergeInputReceipts([]InputReceipt{exact, observed})) != 0 {
		t.Fatal("non-exact evidence upgraded to exact")
	}
}

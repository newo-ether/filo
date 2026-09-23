package codex

import "testing"

func TestUsagePrefersNativeBucketsAndKeepsUnknownWindows(t *testing.T) {
	rpc := &usageFixture{value: map[string]any{"rateLimits": map[string]any{"limitName": "obsolete"}, "rateLimitsByLimitId": map[string]any{"codex": map[string]any{"limitName": "Codex", "primary": map[string]any{"usedPercent": 23.0, "windowDurationMins": 300.0, "resetsAt": 12.0}, "secondary": nil}}}}
	result, err := NewNativeMetadata(rpc).Usage()
	if err != nil || len(result.Limits) != 1 {
		t.Fatal(result, err)
	}
	if rpc.method != "account/rateLimits/read" || result.Limits[0].Name != "Codex" || result.Limits[0].Primary.UsedPercent != 23 || result.Limits[0].Secondary != nil {
		t.Fatal(result)
	}
}

type usageFixture struct {
	method string
	value  any
}

func (f *usageFixture) Request(method string, params any) (any, error) {
	f.method = method
	return f.value, nil
}

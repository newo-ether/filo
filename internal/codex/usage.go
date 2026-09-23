package codex

import (
	"errors"
	"github.com/newo-ether/filo/internal/protocol"
	"sort"
)

// Usage reads the native account API; it cannot consume a reset credit.
func (m *NativeMetadata) Usage() (protocol.AccountUsage, error) {
	value, err := m.rpc.Request("account/rateLimits/read", nil)
	if err != nil {
		return protocol.AccountUsage{}, err
	}
	type bucket struct {
		LimitID   *string               `json:"limitId"`
		LimitName *string               `json:"limitName"`
		Primary   *protocol.UsageWindow `json:"primary"`
		Secondary *protocol.UsageWindow `json:"secondary"`
	}
	var response struct {
		RateLimits *bucket            `json:"rateLimits"`
		ByID       map[string]*bucket `json:"rateLimitsByLimitId"`
	}
	if err := decodeMetadata(value, &response); err != nil {
		return protocol.AccountUsage{}, err
	}
	if len(response.ByID) > 32 {
		return protocol.AccountUsage{}, errors.New("Native usage response is too large")
	}
	result := protocol.AccountUsage{Limits: []protocol.UsageLimit{}}
	appendBucket := func(id string, b *bucket) {
		if b == nil {
			return
		}
		name := id
		if b.LimitName != nil {
			name = *b.LimitName
		}
		result.Limits = append(result.Limits, protocol.UsageLimit{ID: id, Name: name, Primary: b.Primary, Secondary: b.Secondary})
	}
	if len(response.ByID) > 0 {
		keys := make([]string, 0, len(response.ByID))
		for id := range response.ByID {
			keys = append(keys, id)
		}
		sort.Strings(keys)
		for _, id := range keys {
			appendBucket(id, response.ByID[id])
		}
	} else if response.RateLimits != nil {
		id := "codex"
		if response.RateLimits.LimitID != nil {
			id = *response.RateLimits.LimitID
		}
		appendBucket(id, response.RateLimits)
	}
	return result, nil
}

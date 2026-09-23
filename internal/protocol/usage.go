package protocol

type UsageWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins *int64  `json:"windowDurationMins"`
	ResetsAt           *int64  `json:"resetsAt"`
}
type UsageLimit struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Primary   *UsageWindow `json:"primary"`
	Secondary *UsageWindow `json:"secondary"`
}
type AccountUsage struct {
	Limits []UsageLimit `json:"limits"`
}

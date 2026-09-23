// Package protocol defines the bounded public Filo wire format. Native ownership
// and transport authentication belong to their respective adapters.
package protocol

import "encoding/json"

// Optional retains the wire distinction between an absent value and an explicit
// native default (JSON null). In particular, unknown service tier is not default.
type Optional[T any] struct {
	Value T
	Known bool
}

func Known[T any](value T) Optional[T]             { return Optional[T]{Value: value, Known: true} }
func (o Optional[T]) IsZero() bool                 { return !o.Known }
func (o Optional[T]) MarshalJSON() ([]byte, error) { return json.Marshal(o.Value) }
func (o *Optional[T]) UnmarshalJSON(data []byte) error {
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	o.Value, o.Known = value, true
	return nil
}

type Session struct {
	ID        string            `json:"id"`
	Title     Text              `json:"title"`
	Cwd       string            `json:"cwd"`
	UpdatedAt float64           `json:"updatedAt"`
	Status    Optional[*string] `json:"status,omitzero"`
}

type MessageIdentity struct {
	ID            string         `json:"id"`
	TurnID        string         `json:"turnId"`
	GroupID       string         `json:"groupId,omitempty"`
	NativeID      string         `json:"nativeId,omitempty"`
	TextOffset    Optional[int]  `json:"textOffset,omitzero"`
	TextContinues Optional[bool] `json:"textContinues,omitzero"`
	ClientID      *string        `json:"clientId"`
	Role          string         `json:"role"`
	Timestamp     Number         `json:"timestamp"`
	Error         Optional[bool] `json:"error,omitzero"`
}

type Activity struct {
	Type       string            `json:"type"`
	ToolName   string            `json:"toolName,omitempty"`
	Arguments  Optional[Text]    `json:"arguments,omitzero"`
	Result     Optional[Text]    `json:"result,omitzero"`
	State      string            `json:"state,omitempty"`
	DurationMs Optional[float64] `json:"durationMs,omitzero"`
	ImagePath  Optional[string]  `json:"imagePath,omitzero"`
}

type Message struct {
	MessageIdentity
	Text       Text      `json:"text"`
	ImageLinks []string  `json:"imageLinks,omitempty"`
	Activity   *Activity `json:"activity,omitempty"`
}

type ActivityNode struct {
	Type       string            `json:"type"`
	State      string            `json:"state,omitempty"`
	DurationMs Optional[float64] `json:"durationMs,omitzero"`
	HasImage   Optional[bool]    `json:"hasImage,omitzero"`
}

type MessageNode struct {
	MessageIdentity
	ImageCount Optional[int] `json:"imageCount,omitzero"`
	Revision   string        `json:"revision"`
	TextLength int           `json:"textLength"`
	HasContent bool          `json:"hasContent"`
	Activity   *ActivityNode `json:"activity,omitempty"`
}

type SessionStatus struct {
	ID              string  `json:"id"`
	Status          *string `json:"status"`
	ActiveTurnID    *string `json:"activeTurnId"`
	CompletedTurnID *string `json:"completedTurnId"`
	HasUnreadTurn   bool    `json:"hasUnreadTurn"`
}

type SessionPage struct {
	Sessions   []Session       `json:"sessions"`
	NextCursor *string         `json:"nextCursor"`
	Statuses   []SessionStatus `json:"statuses,omitzero"`
}

type QueuedInput struct {
	ID       string `json:"id"`
	ClientID string `json:"clientId"`
	Text     Text   `json:"text"`
}

type Runtime struct {
	Status                   string            `json:"status"`
	ActiveTurnID             *string           `json:"activeTurnId"`
	Model                    *string           `json:"model"`
	ContextTokens            *float64          `json:"contextTokens"`
	ContextWindow            *float64          `json:"contextWindow"`
	CompletedTurnID          Optional[*string] `json:"completedTurnId,omitzero"`
	ActiveTurnHasUserMessage Optional[bool]    `json:"activeTurnHasUserMessage,omitzero"`
	ServiceTierKnown         Optional[bool]    `json:"serviceTierKnown,omitzero"`
	Effort                   Optional[*string] `json:"effort,omitzero"`
	ServiceTier              Optional[*string] `json:"serviceTier,omitzero"`
}

type ConversationPage struct {
	PageCursor Optional[string] `json:"pageCursor,omitzero"`
	Nodes      []MessageNode    `json:"nodes,omitempty"`
	Messages   []Message        `json:"messages"`
	NextCursor *string          `json:"nextCursor"`
	Queued     []QueuedInput    `json:"queued"`
	Runtime    *Runtime         `json:"runtime,omitempty"`
}

// TopologyPage is a ConversationPage stripped of message bodies and the page
// cursor, where the structural node list is required instead of optional.
// Mirrors the TS type Omit<ConversationPage, 'messages' | 'pageCursor'> &
// { nodes: MessageNode[] }; payloads stay on the recoverable payload cache.
type TopologyPage struct {
	Nodes      []MessageNode `json:"nodes"`
	NextCursor *string       `json:"nextCursor"`
	Queued     []QueuedInput `json:"queued"`
	Runtime    *Runtime      `json:"runtime,omitempty"`
}

type ServiceTier struct {
	ID          string `json:"id"`
	Name        Text   `json:"name"`
	Description Text   `json:"description"`
}

type Model struct {
	ID                     string            `json:"id"`
	Name                   Text              `json:"name"`
	IsDefault              bool              `json:"isDefault"`
	ReasoningEfforts       []string          `json:"reasoningEfforts,omitzero"`
	DefaultReasoningEffort Optional[*string] `json:"defaultReasoningEffort,omitzero"`
	ServiceTiers           []ServiceTier     `json:"serviceTiers,omitzero"`
	DefaultServiceTier     Optional[*string] `json:"defaultServiceTier,omitzero"`
}

type SessionSettings struct {
	Model       Optional[string]  `json:"model,omitzero"`
	Effort      Optional[string]  `json:"effort,omitzero"`
	ServiceTier Optional[*string] `json:"serviceTier,omitzero"`
}

type SendReceipt struct {
	TurnID   string `json:"turnId"`
	ClientID string `json:"clientId"`
}

// ServiceInfo is the immutable capability advertisement spread into the
// /v1/info response. Field order matches the TS `serviceInfo` declaration,
// which is also the JSON.stringify key order current clients observe.
type ServiceInfo struct {
	ProtocolVersion      int    `json:"protocolVersion"`
	Agent                string `json:"agent"`
	SessionMode          string `json:"sessionMode"`
	MessageDelivery      string `json:"messageDelivery"`
	OutputMode           string `json:"outputMode"`
	SupportsStop         bool   `json:"supportsStop"`
	SupportsApprovals    bool   `json:"supportsApprovals"`
	SupportsActivity     bool   `json:"supportsActivity"`
	SupportsSettings     bool   `json:"supportsSettings"`
	SupportsLazyMessages bool   `json:"supportsLazyMessages"`
}

// CodexServiceInfo mirrors the TS constant `serviceInfo`.
var CodexServiceInfo = ServiceInfo{
	ProtocolVersion:      2,
	Agent:                "codex",
	SessionMode:          "existing",
	MessageDelivery:      "native-steer",
	OutputMode:           "live-messages",
	SupportsStop:         true,
	SupportsApprovals:    false,
	SupportsActivity:     true,
	SupportsSettings:     true,
	SupportsLazyMessages: true,
}

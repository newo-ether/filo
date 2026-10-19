package protocol

import "time"

// TaskRequestTimeouts bounds outer requests around cold executor discovery and
// protected native creation. These durations never travel on the wire.
type TaskRequestTimeouts struct {
	Read          time.Duration
	ExecutorReady time.Duration
	Create        time.Duration
	Mutation      time.Duration
}

// DefaultTaskRequestTimeouts includes cold executor discovery and protected
// native creation within the outer mutation budget.
var DefaultTaskRequestTimeouts = TaskRequestTimeouts{
	Read:          15 * time.Second,
	ExecutorReady: 90 * time.Second,
	Create:        60 * time.Second,
	Mutation:      180 * time.Second,
}

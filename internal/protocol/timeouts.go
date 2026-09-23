package protocol

import "time"

// TaskRequestTimeouts mirrors the TS `taskRequestTimeouts` constant from
// packages/protocol/src/request-timeouts.ts. These bound outer request
// budgets around cold executor discovery and protected native creation; they
// never travel on the wire, so they are durations rather than millisecond
// numbers.
type TaskRequestTimeouts struct {
	Read          time.Duration
	ExecutorReady time.Duration
	Create        time.Duration
	Mutation      time.Duration
}

// DefaultTaskRequestTimeouts equals the TS constant: readMs 15_000,
// executorReadyMs 90_000, createMs 60_000, mutationMs 180_000. The comment
// above the TS constant notes that outer mutation requests deliberately
// include cold executor discovery plus protected native creation.
var DefaultTaskRequestTimeouts = TaskRequestTimeouts{
	Read:          15 * time.Second,
	ExecutorReady: 90 * time.Second,
	Create:        60 * time.Second,
	Mutation:      180 * time.Second,
}

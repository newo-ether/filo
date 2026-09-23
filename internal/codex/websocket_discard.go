package codex

import "errors"

// ReadSmallMessage consumes oversized native notifications without allocating
// their bodies. A lifetime observer needs only bounded metadata; dropping a tool
// body must not drop its subscription to the native task.
func (ws *WebSocket) ReadSmallMessage(limit int) ([]byte, error) {
	if limit <= 0 || limit > ws.messageLimit() {
		return nil, errors.New("Invalid observer message bound")
	}
	for {
		var body []byte
		started, oversized := false, false
		for {
			remaining := limit - len(body)
			if oversized {
				remaining = 0
			}
			final, opcode, payload, err := ws.readFrameBounded(remaining, true)
			if errors.Is(err, errDiscardedFrame) {
				oversized, body, err = true, nil, nil
			}
			if err != nil {
				return nil, err
			}
			switch opcode {
			case opPing:
				if err := ws.writeFrame(opPong, payload); err != nil {
					return nil, err
				}
				continue
			case opPong:
				continue
			case opClose:
				_ = ws.writeFrame(opClose, payload)
				ws.Terminate()
				return nil, ErrWebSocketClosed
			case opContinuation:
				if !started {
					return nil, errors.New("Unexpected WebSocket continuation")
				}
			case opText, opBinary:
				if started {
					return nil, errors.New("Interleaved WebSocket message")
				}
				started = true
			default:
				return nil, errors.New("Unsupported WebSocket opcode")
			}
			if !oversized {
				body = append(body, payload...)
			}
			if final {
				break
			}
		}
		if !oversized {
			return body, nil
		}
	}
}

var errDiscardedFrame = errors.New("Observer frame discarded")

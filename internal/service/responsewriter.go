package service

import (
	"errors"
	"net/http"
	"time"
)

// Each write has a fresh deadline. A long native operation or a healthy event
// stream may run indefinitely, but a client cannot retain resources by not reading.
const responseWriteTimeout = 15 * time.Second

type deadlineResponseWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
	timeout    time.Duration
	err        error
}

func newDeadlineResponseWriter(w http.ResponseWriter, timeout time.Duration) *deadlineResponseWriter {
	return &deadlineResponseWriter{ResponseWriter: w, controller: http.NewResponseController(w), timeout: timeout}
}

func (w *deadlineResponseWriter) prepare() error {
	if w.err != nil {
		return w.err
	}
	if err := w.controller.SetWriteDeadline(time.Now().Add(w.timeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		w.err = err
	}
	return w.err
}

func (w *deadlineResponseWriter) WriteHeader(status int) {
	if w.prepare() == nil {
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *deadlineResponseWriter) Write(data []byte) (int, error) {
	if err := w.prepare(); err != nil {
		return 0, err
	}
	n, err := w.ResponseWriter.Write(data)
	if err != nil {
		w.err = err
	}
	return n, err
}

func (w *deadlineResponseWriter) FlushError() error {
	if err := w.prepare(); err != nil {
		return err
	}
	if err := w.controller.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		w.err = err
	}
	return w.err
}

func (w *deadlineResponseWriter) Flush()                      { _ = w.FlushError() }
func (w *deadlineResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *deadlineResponseWriter) finish() {
	// net/http also owns connection reuse; do not leave a response's deadline on
	// the next request after a successful short response.
	_ = w.controller.SetWriteDeadline(time.Time{})
}

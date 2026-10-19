package codex

// TrimJSWhitespace exposes the JavaScript String.prototype.trim whitespace set
// (including U+FEFF and the U+2028/U+2029 line separators) to service-layer
// projections that apply JavaScript-compatible truthiness checks to text.
func TrimJSWhitespace(value string) string { return trimJSWhitespace(value) }

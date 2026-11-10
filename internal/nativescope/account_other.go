//go:build !darwin

package nativescope

func validAccountIdentity(value string) bool { return sidPattern.MatchString(value) }

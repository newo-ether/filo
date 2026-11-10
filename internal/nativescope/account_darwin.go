package nativescope

import "strconv"

func validAccountIdentity(value string) bool {
	uid, err := strconv.Atoi(value)
	// Retain existing SID fixtures; callers verify the actual user identity.
	return sidPattern.MatchString(value) || (err == nil && uid > 0 && strconv.Itoa(uid) == value)
}

//go:build !darwin

package service

func validSourceIdentity(value string) bool { return systemServiceSIDPattern.MatchString(value) }

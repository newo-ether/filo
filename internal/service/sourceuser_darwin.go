package service

import (
	"fmt"
	"os"
	"strconv"
)

func validSourceIdentity(value string) bool {
	uid, err := strconv.Atoi(value)
	return err == nil && uid > 0 && strconv.Itoa(uid) == value
}

func verifySourceUser(value string) error {
	if !validSourceIdentity(value) || value != strconv.Itoa(os.Geteuid()) {
		return fmt.Errorf("FILO_SOURCE_UID: helper must run as selected macOS user")
	}
	return nil
}

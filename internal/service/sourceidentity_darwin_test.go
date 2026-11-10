package service

import (
	"os"
	"strconv"
)

func testSourceIdentity() string { return strconv.Itoa(os.Geteuid()) }

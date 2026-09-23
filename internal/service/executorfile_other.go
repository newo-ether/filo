//go:build !windows

package service

import "os"

func openExecutorFile(path string) (*os.File, error) { return os.Open(path) }

func renameExecutorFile(source, target string) error { return os.Rename(source, target) }

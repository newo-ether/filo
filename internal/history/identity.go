package history

import (
	"context"
	"errors"
	"os"
)

// OpenIdentityFile pins the freshly selected compatible native database with a
// read-only handle. Callers must close it and must not publish its private name.
// It grants no native session or mutation authority.
func OpenIdentityFile(ctx context.Context, home string, kind DatabaseKind) (*os.File, error) {
	path, family, err := discoverDatabase(ctx, home, kind)
	if err != nil {
		return nil, err
	}
	file, err := openIdentityFile(path)
	if err != nil {
		return nil, err
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = file.Close()
		}
	}()
	pinned, err := file.Stat()
	if err != nil || !pinned.Mode().IsRegular() {
		return nil, errors.New("Native database identity is unavailable")
	}
	db, err := openDatabase(ctx, path, kind, family)
	if err != nil {
		return nil, err
	}
	if err := db.Close(); err != nil {
		return nil, err
	}
	// Recheck discovery after validation so a newly selected version or a path
	// replacement cannot inherit the identity of the earlier handle.
	current, _, err := discoverDatabase(ctx, home, kind)
	if err != nil {
		return nil, err
	}
	if current != path {
		return nil, errors.New("Native database changed during discovery")
	}
	latest, err := os.Stat(current)
	if err != nil || !os.SameFile(pinned, latest) {
		return nil, errors.New("Native database changed during discovery")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	accepted = true
	return file, nil
}

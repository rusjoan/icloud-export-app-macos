package main

import (
	"context"
	"errors"

	"icloud-export/internal/icloud"
)

func runApp(ctx context.Context, sess *icloud.Session, browser bool) error {
	return errors.New("интерфейс ещё не готов, используйте: icloud-export probe")
}

//go:build !darwin

package meta

import "time"

// Вне macOS дату создания выставить нельзя; нужна только для тестов в контейнере.
func setBirthtime(string, time.Time) error { return nil }

//go:build !darwin

package macwindow

import "errors"

var errUnsupported = errors.New("нативное окно есть только в macOS")

func Run(title, url string, quit func()) error { return errUnsupported }

func PickFolder() (string, error) { return "", errUnsupported }

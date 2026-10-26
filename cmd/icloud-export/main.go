package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"

	"github.com/rusjoan/icloud-export-app-macos/internal/icloud"
)

var version = "dev"

func main() {
	browser := flag.Bool("browser", false, "открыть интерфейс в браузере вместо собственного окна")
	serve := flag.Bool("serve", false, "только запустить сервер и напечатать адрес интерфейса (для отладки)")
	appleID := flag.String("apple-id", "", "Apple ID (для probe)")
	addr := flag.String("addr", "127.0.0.1:0", "адрес интерфейса; порт можно задать для SSH-туннеля, хост — только 127.0.0.1")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	setupLog()
	log.Printf("icloud-export %s запущен", version)
	icloud.Logf = log.Printf
	sess := icloud.NewSession(filepath.Join(appSupportDir(), "session.json"))

	var err error
	switch flag.Arg(0) {
	case "probe":
		err = probe(ctx, sess, *appleID)
	case "version":
		fmt.Println(version)
	default:
		err = runApp(ctx, sess, *browser, *serve, *addr)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

// setupLog пишет журнал в ~/Library/Logs/icloud-export.log (и в stderr).
func setupLog() {
	dir := logDir()
	_ = os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "icloud-export.log")
	if fi, err := os.Stat(path); err == nil && fi.Size() > 5<<20 {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
}

// appSupportDir — macOS: ~/Library/Application Support/icloud-export, Linux: ~/.config/icloud-export.
func appSupportDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "icloud-export")
}

// logDir — macOS: ~/Library/Logs, Linux: $XDG_STATE_HOME/icloud-export (~/.local/state/icloud-export).
func logDir() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Logs")
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "icloud-export")
	}
	return filepath.Join(home, ".local", "state", "icloud-export")
}

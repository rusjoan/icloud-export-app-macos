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

	"icloud-export/internal/icloud"
)

var version = "dev"

func main() {
	browser := flag.Bool("browser", false, "открыть интерфейс в браузере вместо собственного окна")
	serve := flag.Bool("serve", false, "только запустить сервер и напечатать адрес интерфейса (для отладки)")
	appleID := flag.String("apple-id", "", "Apple ID (для probe)")
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
		err = runApp(ctx, sess, *browser, *serve)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

// setupLog пишет журнал в ~/Library/Logs/icloud-export.log (и в stderr).
func setupLog() {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, "Library", "Logs")
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

func appSupportDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "icloud-export")
}

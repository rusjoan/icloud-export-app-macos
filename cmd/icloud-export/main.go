package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"icloud-export/internal/icloud"
)

var version = "dev"

func main() {
	browser := flag.Bool("browser", false, "открыть интерфейс в браузере вместо собственного окна")
	appleID := flag.String("apple-id", "", "Apple ID (для probe)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	sess := icloud.NewSession(filepath.Join(appSupportDir(), "session.json"))

	var err error
	switch flag.Arg(0) {
	case "probe":
		err = probe(ctx, sess, *appleID)
	case "version":
		fmt.Println(version)
	default:
		err = runApp(ctx, sess, *browser)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

func appSupportDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "icloud-export")
}

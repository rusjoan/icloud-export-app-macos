package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/rusjoan/icloud-export-app-macos/internal/icloud"
	"github.com/rusjoan/icloud-export-app-macos/internal/ui/macwindow"
	"github.com/rusjoan/icloud-export-app-macos/internal/web"
)

func runApp(ctx context.Context, sess *icloud.Session, browser, serve bool, addr string) error {
	native := runtime.GOOS == "darwin" && !browser && !serve
	if runtime.GOOS != "darwin" && !serve {
		// собственного окна вне macOS нет — открываем в браузере, если есть графика
		browser = os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
		serve = !browser
	}
	srv := web.New(ctx, sess)
	srv.Version = version
	srv.Addr = addr
	if native {
		srv.Picker = macwindow.PickFolder
	}
	url, err := srv.Listen()
	if err != nil {
		return err
	}
	quit := func() {
		srv.Close() // останавливает выгрузку, индекс сохраняется
		_ = sess.Save()
		os.Exit(0)
	}
	if !native {
		fmt.Println("Интерфейс:", url)
		if browser {
			_ = exec.Command(web.Opener(), url).Run()
		}
		<-ctx.Done()
		quit()
	}
	go func() {
		<-ctx.Done() // Ctrl+C в терминале
		quit()
	}()
	return macwindow.Run("iCloud Export", url, quit)
}

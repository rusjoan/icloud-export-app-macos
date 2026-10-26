package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"icloud-export/internal/icloud"
	"icloud-export/internal/ui/macwindow"
	"icloud-export/internal/web"
)

func runApp(ctx context.Context, sess *icloud.Session, browser bool) error {
	srv := web.New(ctx, sess)
	srv.Version = version
	if !browser {
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
	if browser {
		fmt.Println("Интерфейс:", url)
		_ = exec.Command("open", url).Run()
		<-ctx.Done()
		quit()
	}
	go func() {
		<-ctx.Done() // Ctrl+C в терминале
		quit()
	}()
	return macwindow.Run("iCloud Export", url, quit)
}

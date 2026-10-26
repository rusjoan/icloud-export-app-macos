package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/rusjoan/icloud-export-app-macos/internal/icloud"
)

// probe — проверка входа и доступа к альбомам из терминала, без UI.
func probe(ctx context.Context, sess *icloud.Session, appleID string) error {
	in := bufio.NewReader(os.Stdin)
	if err := sess.Restore(ctx); err != nil {
		if appleID == "" {
			fmt.Print("Apple ID: ")
			appleID, _ = in.ReadString('\n')
			appleID = strings.TrimSpace(appleID)
		}
		fmt.Print("Пароль Apple ID (не пароль приложения): ")
		pw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return err
		}
		err = sess.Login(ctx, appleID, string(pw))
		if errors.Is(err, icloud.ErrNeed2FA) {
			fmt.Print("Код 2FA с устройства: ")
			code, _ := in.ReadString('\n')
			err = sess.Verify2FA(ctx, code, 0)
		}
		if err != nil {
			return err
		}
	}
	acc := sess.Account()
	fmt.Printf("Вход выполнен: %s (dsid %s)\n", acc.AppleID, acc.DSID)

	ph, err := icloud.NewPhotos(ctx, sess)
	if err != nil {
		return err
	}
	albums, err := ph.Albums(ctx)
	if err != nil {
		return err
	}
	for _, a := range albums {
		fmt.Printf("  %-8s %6d  %s\n", a.Kind, a.Count, a.Name)
	}
	for _, a := range albums {
		if a.Kind != icloud.KindHidden {
			continue
		}
		n := 0
		errStop := errors.New("stop")
		err := ph.Assets(ctx, a, func(as icloud.Asset) error {
			res := as.Original
			if as.Adjusted {
				res = as.Edited
			}
			fmt.Printf("  скрытое: %s  %s  %d байт  правки=%v  live=%v\n",
				as.Filename, as.LocalDate().Format("2006-01-02 15:04 -07:00"), res.Size, as.Adjusted, as.OrigLive != nil)
			if n++; n >= 3 {
				return errStop
			}
			return nil
		})
		if err != nil && !errors.Is(err, errStop) {
			return err
		}
	}
	return nil
}

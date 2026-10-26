// Package macwindow — нативное окно приложения (NSWindow + WKWebView) без cgo,
// через Objective-C runtime (purego/objc). Работает только на главном потоке.
package macwindow

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

func init() {
	// AppKit требует главный поток: закрепляем за ним главную горутину.
	runtime.LockOSThread()
}

type cgPoint struct{ X, Y float64 }
type cgSize struct{ W, H float64 }
type cgRect struct {
	Origin cgPoint
	Size   cgSize
}

const (
	styleTitled         = 1 << 0
	styleClosable       = 1 << 1
	styleMiniaturizable = 1 << 2
	styleResizable      = 1 << 3
	backingBuffered     = 2
	autoresizeWH        = 2 | 16 // NSViewWidthSizable | NSViewHeightSizable
	utf8Encoding        = 4
	modalResponseOK     = 1
	terminateCancel     = 0
)

var (
	loadOnce sync.Once
	loadErr  error

	dispatcher objc.ID
	mainQueue  = make(chan func(), 16)
	onQuit     func()
)

func sel(name string) objc.SEL { return objc.RegisterName(name) }

func class(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

func load() error {
	loadOnce.Do(func() {
		for _, fw := range []string{
			"/System/Library/Frameworks/Foundation.framework/Foundation",
			"/System/Library/Frameworks/AppKit.framework/AppKit",
			"/System/Library/Frameworks/WebKit.framework/WebKit",
		} {
			if _, err := purego.Dlopen(fw, purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
				loadErr = err
				return
			}
		}
		loadErr = registerClasses()
	})
	return loadErr
}

func registerClasses() error {
	// делегат приложения: закрытие окна завершает приложение, но через Go (onQuit),
	// чтобы успеть остановить выгрузку и сохранить индекс
	_, err := objc.RegisterClass("IEAppDelegate", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{Cmd: sel("applicationShouldTerminateAfterLastWindowClosed:"), Fn: func(self objc.ID, cmd objc.SEL, app objc.ID) bool {
			return true
		}},
		{Cmd: sel("applicationShouldTerminate:"), Fn: func(self objc.ID, cmd objc.SEL, app objc.ID) uint {
			if onQuit != nil {
				go onQuit()
			}
			return terminateCancel
		}},
	})
	if err != nil {
		return err
	}
	_, err = objc.RegisterClass("IEDispatcher", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{Cmd: sel("run:"), Fn: func(self objc.ID, cmd objc.SEL, arg objc.ID) {
			for {
				select {
				case f := <-mainQueue:
					f()
				default:
					return
				}
			}
		}},
	})
	return err
}

func nsString(s string) objc.ID {
	b := []byte(s)
	if len(b) == 0 {
		return class("NSString").Send(sel("string"))
	}
	str := class("NSString").Send(sel("alloc")).Send(sel("initWithBytes:length:encoding:"),
		unsafe.Pointer(&b[0]), uint(len(b)), uint(utf8Encoding))
	runtime.KeepAlive(b)
	return str.Send(sel("autorelease"))
}

func goString(ns objc.ID) string {
	if ns == 0 {
		return ""
	}
	p := objc.Send[*byte](ns, sel("UTF8String"))
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
		n++
	}
	return string(unsafe.Slice(p, n))
}

func menuItem(title, action, key string) objc.ID {
	var act objc.SEL
	if action != "" {
		act = sel(action)
	}
	return class("NSMenuItem").Send(sel("alloc")).Send(sel("initWithTitle:action:keyEquivalent:"),
		nsString(title), act, nsString(key))
}

func buildMenu(app objc.ID, appName string) {
	bar := class("NSMenu").Send(sel("new"))

	appItem := class("NSMenuItem").Send(sel("new"))
	bar.Send(sel("addItem:"), appItem)
	appMenu := class("NSMenu").Send(sel("alloc")).Send(sel("initWithTitle:"), nsString(appName))
	appMenu.Send(sel("addItem:"), menuItem("Скрыть "+appName, "hide:", "h"))
	appMenu.Send(sel("addItem:"), class("NSMenuItem").Send(sel("separatorItem")))
	appMenu.Send(sel("addItem:"), menuItem("Завершить "+appName, "terminate:", "q"))
	appItem.Send(sel("setSubmenu:"), appMenu)

	editItem := class("NSMenuItem").Send(sel("new"))
	bar.Send(sel("addItem:"), editItem)
	edit := class("NSMenu").Send(sel("alloc")).Send(sel("initWithTitle:"), nsString("Правка"))
	for _, it := range [][3]string{
		{"Отменить", "undo:", "z"}, {"Повторить", "redo:", "Z"}, {"", "", ""},
		{"Вырезать", "cut:", "x"}, {"Скопировать", "copy:", "c"}, {"Вставить", "paste:", "v"},
		{"Выбрать все", "selectAll:", "a"},
	} {
		if it[0] == "" {
			edit.Send(sel("addItem:"), class("NSMenuItem").Send(sel("separatorItem")))
			continue
		}
		edit.Send(sel("addItem:"), menuItem(it[0], it[1], it[2]))
	}
	editItem.Send(sel("setSubmenu:"), edit)

	app.Send(sel("setMainMenu:"), bar)
}

// Run показывает окно с url и крутит цикл событий AppKit (не возвращается, пока
// приложение живо). quit вызывается при закрытии окна или Cmd+Q — он должен
// завершить процесс сам.
func Run(title, url string, quit func()) error {
	if err := load(); err != nil {
		return err
	}
	onQuit = quit
	pool := class("NSAutoreleasePool").Send(sel("new"))
	defer pool.Send(sel("drain"))

	app := class("NSApplication").Send(sel("sharedApplication"))
	app.Send(sel("setActivationPolicy:"), 0) // NSApplicationActivationPolicyRegular
	app.Send(sel("setDelegate:"), class("IEAppDelegate").Send(sel("new")))
	dispatcher = class("IEDispatcher").Send(sel("new"))
	buildMenu(app, title)

	rect := cgRect{Size: cgSize{W: 1100, H: 760}}
	win := class("NSWindow").Send(sel("alloc")).Send(sel("initWithContentRect:styleMask:backing:defer:"),
		rect, uint(styleTitled|styleClosable|styleMiniaturizable|styleResizable), uint(backingBuffered), false)
	win.Send(sel("setTitle:"), nsString(title))
	win.Send(sel("setContentMinSize:"), cgSize{W: 760, H: 520})
	win.Send(sel("setReleasedWhenClosed:"), false)

	cfg := class("WKWebViewConfiguration").Send(sel("new"))
	prefs := cfg.Send(sel("preferences"))
	prefs.Send(sel("setValue:forKey:"), class("NSNumber").Send(sel("numberWithBool:"), true), nsString("developerExtrasEnabled"))
	web := class("WKWebView").Send(sel("alloc")).Send(sel("initWithFrame:configuration:"), rect, cfg)
	web.Send(sel("setAutoresizingMask:"), uint(autoresizeWH))
	win.Send(sel("setContentView:"), web)

	nsurl := class("NSURL").Send(sel("URLWithString:"), nsString(url))
	if nsurl == 0 {
		return errors.New("некорректный URL интерфейса")
	}
	web.Send(sel("loadRequest:"), class("NSURLRequest").Send(sel("requestWithURL:"), nsurl))

	win.Send(sel("center"))
	win.Send(sel("makeKeyAndOrderFront:"), objc.ID(0))
	app.Send(sel("activateIgnoringOtherApps:"), true)
	app.Send(sel("run"))
	return nil
}

// onMain выполняет f на главном потоке и ждёт завершения.
func onMain(f func()) {
	done := make(chan struct{})
	mainQueue <- func() { f(); close(done) }
	dispatcher.Send(sel("performSelectorOnMainThread:withObject:waitUntilDone:"), sel("run:"), objc.ID(0), false)
	<-done
}

// PickFolder показывает системный диалог выбора папки ("" — отменено).
func PickFolder() (string, error) {
	if dispatcher == 0 {
		return "", errors.New("окно приложения не запущено")
	}
	var path string
	onMain(func() {
		panel := class("NSOpenPanel").Send(sel("openPanel"))
		panel.Send(sel("setCanChooseDirectories:"), true)
		panel.Send(sel("setCanChooseFiles:"), false)
		panel.Send(sel("setCanCreateDirectories:"), true)
		panel.Send(sel("setAllowsMultipleSelection:"), false)
		panel.Send(sel("setPrompt:"), nsString("Выбрать"))
		panel.Send(sel("setMessage:"), nsString("Папка для выгрузки фото"))
		if objc.Send[int](panel, sel("runModal")) == modalResponseOK {
			path = goString(panel.Send(sel("URL")).Send(sel("path")))
		}
	})
	return path, nil
}

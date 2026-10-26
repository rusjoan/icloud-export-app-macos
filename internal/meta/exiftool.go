package meta

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// Приложение, запущенное из Finder, получает PATH без Homebrew, поэтому
// кроме PATH смотрим стандартные места.
var searchDirs = []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"}

// FindTool ищет исполняемый файл в PATH и стандартных каталогах Homebrew.
func FindTool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, d := range searchDirs {
		p := d + "/" + name
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// ExifToolVersion возвращает версию exiftool по пути (пусто — не работает).
func ExifToolVersion(path string) string {
	out, err := exec.Command(path, "-ver").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ExifTool — долгоживущий процесс exiftool -stay_open.
type ExifTool struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *bufio.Reader
	seq    int
}

func StartExifTool(path string) (*ExifTool, error) {
	cmd := exec.Command(path, "-stay_open", "True", "-@", "-")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &ExifTool{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout), stderr: bufio.NewReader(stderr)}, nil
}

// Write выполняет команду записи; ошибка — файл не обновлён.
func (e *ExifTool) Write(path string, args []string) error {
	out, errOut, err := e.Execute(args...)
	if err != nil {
		return err
	}
	if strings.Contains(out, "1 image files updated") || strings.Contains(out, "1 image files unchanged") {
		return nil
	}
	msg := strings.TrimSpace(errOut)
	if msg == "" {
		msg = strings.TrimSpace(out)
	}
	return fmt.Errorf("exiftool не записал %s: %s", path, msg)
}

// Execute передаёт аргументы exiftool и ждёт завершения команды.
func (e *ExifTool) Execute(args ...string) (stdout, stderr string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cmd == nil {
		return "", "", errors.New("exiftool остановлен")
	}
	e.seq++
	marker := "{ready" + strconv.Itoa(e.seq) + "}"
	var b strings.Builder
	for _, a := range args {
		b.WriteString(argLine(a))
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "-echo4\n%s\n-execute%d\n", marker, e.seq)
	if _, err := io.WriteString(e.stdin, b.String()); err != nil {
		return "", "", err
	}
	if stdout, err = readUntil(e.stdout, marker); err != nil {
		return stdout, "", err
	}
	stderr, err = readUntil(e.stderr, marker)
	return stdout, stderr, err
}

// argLine кодирует аргумент для argfile: переводы строк — через #[CSTR].
func argLine(a string) string {
	if !strings.ContainsAny(a, "\n\r") {
		return a
	}
	r := strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return "#[CSTR]" + r.Replace(a)
}

func readUntil(r *bufio.Reader, marker string) (string, error) {
	var b strings.Builder
	for {
		line, err := r.ReadString('\n')
		if strings.TrimRight(line, "\r\n") == marker {
			return b.String(), nil
		}
		b.WriteString(line)
		if err != nil {
			return b.String(), err
		}
	}
}

func (e *ExifTool) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cmd == nil {
		return nil
	}
	_, _ = io.WriteString(e.stdin, "-stay_open\nFalse\n")
	_ = e.stdin.Close()
	err := e.cmd.Wait()
	e.cmd = nil
	return err
}

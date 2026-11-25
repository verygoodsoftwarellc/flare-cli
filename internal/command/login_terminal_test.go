//go:build linux

package command

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/verygoodsoftwarellc/flare-cli/internal/config"
	"github.com/zalando/go-keyring"
)

// A terminal never closes stdin, so a pasted token has to end at Enter, and
// it shouldn't stay on the screen.
func TestLoginWithTokenReadsAHiddenLineFromATerminal(t *testing.T) {
	login := startTerminalLogin(t)
	if _, err := login.keyboard.Write([]byte("flare_pat_typed\r")); err != nil {
		t.Fatal(err)
	}
	if err := login.wait(t); err != nil {
		t.Fatal(err)
	}

	login.terminal.Close()
	shown, _ := io.ReadAll(login.keyboard) // ends with EIO once the terminal side is closed
	if strings.Contains(string(shown), "flare_pat_") {
		t.Fatalf("the terminal showed the token: %q", shown)
	}
	if !strings.Contains(login.stderr.String(), "Paste your Flare personal access token: ") {
		t.Fatalf("stderr = %q", login.stderr.String())
	}
	if saved, err := login.settings(t).TokenValue(); err != nil || saved != "flare_pat_typed" {
		t.Fatalf("saved %q (%v)", saved, err)
	}
}

// Ctrl-C has to leave the terminal showing what's typed again.
func TestLoginWithTokenCtrlCPutsTheTerminalBack(t *testing.T) {
	login := startTerminalLogin(t)
	if _, err := login.keyboard.Write([]byte("flare_pat_\x03")); err != nil {
		t.Fatal(err)
	}
	if err := login.wait(t); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("err = %v", err)
	}
	if !echoing(t, login.terminal) {
		t.Fatal("the terminal still hides what's typed")
	}
	if saved, err := login.settings(t).TokenValue(); err == nil {
		t.Fatalf("saved %q", saved)
	}
}

type terminalLogin struct {
	keyboard, terminal *os.File
	stderr             *bytes.Buffer
	apiURL             string
	done               chan error
}

// startTerminalLogin runs `auth login --with-token` on a pseudo-terminal and
// returns once the terminal stops echoing, as someone would wait for a prompt.
func startTerminalLogin(t *testing.T) *terminalLogin {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	keyring.MockInit()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	keyboard, terminal := openTerminal(t)
	login := &terminalLogin{keyboard: keyboard, terminal: terminal, stderr: &bytes.Buffer{}, apiURL: server.URL, done: make(chan error, 1)}
	options := &rootOptions{name: "flare-cli", apiURL: server.URL, output: &bytes.Buffer{}, errors: login.stderr, input: terminal}
	command := newAuthCommand(options)
	command.SetArgs([]string{"login", "--with-token"})
	command.SilenceUsage = true
	command.SilenceErrors = true
	go func() { login.done <- command.Execute() }()
	for start := time.Now(); echoing(t, terminal) && time.Since(start) < 5*time.Second; {
		time.Sleep(10 * time.Millisecond)
	}
	return login
}

func (login *terminalLogin) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-login.done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("login --with-token is still waiting")
		return nil
	}
}

func (login *terminalLogin) settings(t *testing.T) *config.Config {
	t.Helper()
	settings, err := config.Load(login.apiURL)
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

// openTerminal opens a pseudo-terminal: what's written to keyboard is typed
// into terminal, and keyboard reads what terminal displays.
func openTerminal(t *testing.T) (keyboard, terminal *os.File) {
	t.Helper()
	keyboard, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pseudo-terminals: %v", err)
	}
	t.Cleanup(func() { keyboard.Close() })
	var unlock int32
	ioctl(t, keyboard, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock))
	var number uint32
	ioctl(t, keyboard, syscall.TIOCGPTN, unsafe.Pointer(&number))
	terminal, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { terminal.Close() })
	return keyboard, terminal
}

func echoing(t *testing.T, terminal *os.File) bool {
	t.Helper()
	var state syscall.Termios
	ioctl(t, terminal, syscall.TCGETS, unsafe.Pointer(&state))
	return state.Lflag&syscall.ECHO != 0
}

func ioctl(t *testing.T, file *os.File, request uintptr, argument unsafe.Pointer) {
	t.Helper()
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), request, uintptr(argument)); errno != 0 {
		t.Fatal(errno)
	}
}

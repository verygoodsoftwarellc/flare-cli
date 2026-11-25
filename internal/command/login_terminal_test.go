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
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	keyring.MockInit()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()
	keyboard, terminal := openTerminal(t)

	stderr := &bytes.Buffer{}
	options := &rootOptions{name: "flare-cli", apiURL: server.URL, output: &bytes.Buffer{}, errors: stderr, input: terminal}
	command := newAuthCommand(options)
	command.SetArgs([]string{"login", "--with-token"})
	done := make(chan error, 1)
	go func() { done <- command.Execute() }()

	// Type once the terminal stops echoing, as someone would at a prompt.
	for start := time.Now(); echoing(t, terminal) && time.Since(start) < 5*time.Second; {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := keyboard.Write([]byte("flare_pat_typed\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("login --with-token is still waiting after Enter")
	}

	terminal.Close()
	shown, _ := io.ReadAll(keyboard) // ends with EIO once the terminal side is closed
	if strings.Contains(string(shown), "flare_pat_") {
		t.Fatalf("the terminal showed the token: %q", shown)
	}
	if !strings.Contains(stderr.String(), "Paste your Flare personal access token: ") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	settings, err := config.Load(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if saved, err := settings.TokenValue(); err != nil || saved != "flare_pat_typed" {
		t.Fatalf("saved %q (%v)", saved, err)
	}
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

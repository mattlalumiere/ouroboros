package app

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"golang.org/x/term"
)

// Passphrase gets a passphrase for command line use. OUROBOROS_PASSPHRASE wins
// when set, otherwise it is read from the terminal without echo.
func Passphrase(confirm bool) (string, error) {
	if v := os.Getenv("OUROBOROS_PASSPHRASE"); v != "" {
		return v, nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", errors.New("no passphrase given: set OUROBOROS_PASSPHRASE or use a terminal")
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	read := func(prompt string) (string, error) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	p, err := read("Passphrase: ")
	if err != nil {
		return "", err
	}
	if confirm {
		again, err := read("Repeat passphrase: ")
		if err != nil {
			return "", err
		}
		if again != p {
			return "", errors.New("the passphrases do not match")
		}
	}
	return p, nil
}

// Fatal prints the error and exits. On Windows it waits for Enter first, since
// a double clicked program would otherwise close its console window at once.
func Fatal(err error) {
	fmt.Fprintln(os.Stderr, "Error:", err)
	if runtime.GOOS == "windows" {
		fmt.Fprint(os.Stderr, "Press Enter to close this window.")
		bufio.NewReader(os.Stdin).ReadString('\n')
	}
	os.Exit(1)
}

// Package prompt reads passwords from the terminal without echoing them, or
// from standard input when it isn't a terminal.
package prompt

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// IsTerminal reports whether standard input is a terminal that can be
// prompted.
func IsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// Password prompts for a password, as "<name>: ".
func Password(name string) (string, error) {
	fmt.Fprintf(os.Stderr, "%s: ", name)
	password, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(password), err
}

// NewPassword prompts for a password twice, as "New <name>: " and
// "Confirm <name>: ", and fails if the two don't match.
func NewPassword(name string) (string, error) {
	password, err := Password("New " + name)
	if err != nil {
		return "", err
	}
	confirm, err := Password("Confirm " + name)
	if err != nil {
		return "", err
	}
	if password != confirm {
		return "", errors.New("passwords do not match")
	}
	return password, nil
}

// ReadNewPassword prompts for a new password with NewPassword if standard
// input is a terminal, and otherwise reads it from the first line of standard
// input, so it can be piped in from a password manager or script. It fails if
// the password is empty.
func ReadNewPassword(name string) (password string, err error) {
	if IsTerminal() {
		password, err = NewPassword(name)
	} else {
		password, err = Line(os.Stdin)
	}
	if err == nil && password == "" {
		err = fmt.Errorf("%s cannot be empty", name)
	}
	return password, err
}

// Line reads the first line of r, without its line ending.
func Line(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

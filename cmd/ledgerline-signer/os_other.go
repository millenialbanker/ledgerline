//go:build !windows

package main

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// Outside Windows there is no USB token support (use -pfx) and no DPAPI:
// the agent key is kept as it is, in a file only this user can read.

const tokenSupported = false

func protect(b []byte) (string, error) { return base64.StdEncoding.EncodeToString(b), nil }

func unprotect(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

func chooseCert(*bufio.Reader) (string, error) {
	return "", errors.New("USB tokens are read on Windows only; give a certificate file with -pfx")
}

func openToken(string, string) (backend, error) {
	return nil, errors.New("USB tokens are read on Windows only; give a certificate file with -pfx")
}

// readSecret asks for a password without showing it (stty), falling back
// to a plain read when there is no terminal.
func readSecret(prompt string, in *bufio.Reader) (string, error) {
	fmt.Print(prompt)
	off := exec.Command("stty", "-echo")
	off.Stdin = os.Stdin
	hidden := off.Run() == nil
	s, err := readLine(in)
	if hidden {
		on := exec.Command("stty", "echo")
		on.Stdin = os.Stdin
		_ = on.Run()
		fmt.Println()
	}
	return s, err
}

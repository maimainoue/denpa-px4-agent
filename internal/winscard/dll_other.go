//go:build !windows && !darwin

package winscard

import "errors"

// DefaultDLL は OS の PC/SC (Windows は System32 の winscard.dll、macOS は PCSC.framework)。ほかの OS には無い。
func DefaultDLL() string { return "" }

// LoadDLL は Windows と macOS 以外では使えない。
func LoadDLL(path string) (API, error) {
	return nil, errors.New("PC/SC の DLL を読めるのは Windows と macOS だけです")
}

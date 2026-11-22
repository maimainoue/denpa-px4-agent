//go:build !windows

package winscard

import "errors"

// DefaultDLL は System32 の winscard.dll。Windows 以外には無い。
func DefaultDLL() string { return "" }

// LoadDLL は Windows 以外では使えない。
func LoadDLL(path string) (API, error) {
	return nil, errors.New("PC/SC の DLL を読めるのは Windows だけです")
}

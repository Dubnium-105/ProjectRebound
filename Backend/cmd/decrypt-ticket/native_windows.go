//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsTicketLibrary struct {
	dll       *windows.DLL
	decrypt   *windows.Proc
	steamID   *windows.Proc
	appID     *windows.Proc
	issueTime *windows.Proc
	vacBanned *windows.Proc
}

func loadNativeTicketLibrary(path string) (nativeTicketLibrary, error) {
	dll, err := windows.LoadDLL(path)
	if err != nil {
		return nil, err
	}
	library := &windowsTicketLibrary{dll: dll}
	if library.decrypt, err = dll.FindProc("SteamEncryptedAppTicket_BDecryptTicket"); err != nil {
		_ = dll.Release()
		return nil, err
	}
	if library.steamID, err = dll.FindProc("SteamEncryptedAppTicket_GetTicketSteamID"); err != nil {
		_ = dll.Release()
		return nil, err
	}
	if library.appID, err = dll.FindProc("SteamEncryptedAppTicket_GetTicketAppID"); err != nil {
		_ = dll.Release()
		return nil, err
	}
	if library.issueTime, err = dll.FindProc("SteamEncryptedAppTicket_GetTicketIssueTime"); err != nil {
		_ = dll.Release()
		return nil, err
	}
	if library.vacBanned, err = dll.FindProc("SteamEncryptedAppTicket_BUserIsVacBanned"); err != nil {
		_ = dll.Release()
		return nil, err
	}
	return library, nil
}

func defaultNativeTicketLibraryPath() string {
	executable, err := os.Executable()
	if err != nil {
		return "sdkencryptedappticket64.dll"
	}
	return filepath.Join(filepath.Dir(executable), "sdkencryptedappticket64.dll")
}

func (l *windowsTicketLibrary) Decrypt(encryptedTicket []byte, key []byte) ([]byte, error) {
	if len(encryptedTicket) == 0 || len(key) != symmetricKeyBytes {
		return nil, errors.New("invalid decrypt input")
	}
	decrypted := make([]byte, 4096)
	decryptedLength := uint32(len(decrypted))
	result, _, callErr := l.decrypt.Call(
		uintptr(unsafe.Pointer(&encryptedTicket[0])),
		uintptr(len(encryptedTicket)),
		uintptr(unsafe.Pointer(&decrypted[0])),
		uintptr(unsafe.Pointer(&decryptedLength)),
		uintptr(unsafe.Pointer(&key[0])),
		uintptr(len(key)),
	)
	if result == 0 {
		clear(decrypted)
		if callErr != nil && !errors.Is(callErr, windows.ERROR_SUCCESS) {
			return nil, callErr
		}
		return nil, errors.New("SteamEncryptedAppTicket_BDecryptTicket rejected the ticket")
	}
	if decryptedLength == 0 || int(decryptedLength) > len(decrypted) {
		clear(decrypted)
		return nil, errors.New("Steam ticket library returned an invalid plaintext length")
	}
	return decrypted[:decryptedLength], nil
}

func (l *windowsTicketLibrary) SteamID(ticket []byte) (uint64, error) {
	var value uint64
	_, _, callErr := l.steamID.Call(
		uintptr(unsafe.Pointer(&ticket[0])),
		uintptr(len(ticket)),
		uintptr(unsafe.Pointer(&value)),
	)
	if callErr != nil && !errors.Is(callErr, windows.ERROR_SUCCESS) {
		return 0, callErr
	}
	return value, nil
}

func (l *windowsTicketLibrary) AppID(ticket []byte) (uint32, error) {
	value, _, callErr := l.appID.Call(
		uintptr(unsafe.Pointer(&ticket[0])),
		uintptr(len(ticket)),
	)
	if callErr != nil && !errors.Is(callErr, windows.ERROR_SUCCESS) {
		return 0, callErr
	}
	return uint32(value), nil
}

func (l *windowsTicketLibrary) IssueTime(ticket []byte) (uint32, error) {
	value, _, callErr := l.issueTime.Call(
		uintptr(unsafe.Pointer(&ticket[0])),
		uintptr(len(ticket)),
	)
	if callErr != nil && !errors.Is(callErr, windows.ERROR_SUCCESS) {
		return 0, callErr
	}
	return uint32(value), nil
}

func (l *windowsTicketLibrary) VACBanned(ticket []byte) (bool, error) {
	value, _, callErr := l.vacBanned.Call(
		uintptr(unsafe.Pointer(&ticket[0])),
		uintptr(len(ticket)),
	)
	if callErr != nil && !errors.Is(callErr, windows.ERROR_SUCCESS) {
		return false, callErr
	}
	return value != 0, nil
}

func (l *windowsTicketLibrary) Close() error {
	if l.dll == nil {
		return nil
	}
	if err := l.dll.Release(); err != nil {
		return fmt.Errorf("release Steam ticket DLL: %w", err)
	}
	l.dll = nil
	return nil
}

//go:build linux && cgo

package main

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>

typedef bool (*decrypt_fn)(const uint8_t*, uint32_t, uint8_t*, uint32_t*, const uint8_t*, int);
typedef void (*steam_id_fn)(uint8_t*, uint32_t, uint64_t*);
typedef uint32_t (*uint32_fn)(uint8_t*, uint32_t);
typedef bool (*bool_fn)(uint8_t*, uint32_t);

static int call_decrypt(
	void *fn,
	const uint8_t *encrypted,
	uint32_t encrypted_len,
	uint8_t *decrypted,
	uint32_t *decrypted_len,
	const uint8_t *key,
	int key_len
) {
	return ((decrypt_fn)fn)(encrypted, encrypted_len, decrypted, decrypted_len, key, key_len) ? 1 : 0;
}

static void call_steam_id(void *fn, uint8_t *ticket, uint32_t ticket_len, uint64_t *steam_id) {
	((steam_id_fn)fn)(ticket, ticket_len, steam_id);
}

static uint32_t call_uint32(void *fn, uint8_t *ticket, uint32_t ticket_len) {
	return ((uint32_fn)fn)(ticket, ticket_len);
}

static int call_bool(void *fn, uint8_t *ticket, uint32_t ticket_len) {
	return ((bool_fn)fn)(ticket, ticket_len) ? 1 : 0;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

type linuxTicketLibrary struct {
	handle    unsafe.Pointer
	decrypt   unsafe.Pointer
	steamID   unsafe.Pointer
	appID     unsafe.Pointer
	issueTime unsafe.Pointer
	vacBanned unsafe.Pointer
}

func loadNativeTicketLibrary(path string) (nativeTicketLibrary, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	handle := C.dlopen(cPath, C.RTLD_NOW|C.RTLD_LOCAL)
	if handle == nil {
		return nil, fmt.Errorf("dlopen %q: %s", path, C.GoString(C.dlerror()))
	}
	library := &linuxTicketLibrary{handle: handle}
	var err error
	if library.decrypt, err = findSymbol(handle, "SteamEncryptedAppTicket_BDecryptTicket"); err != nil {
		_ = library.Close()
		return nil, err
	}
	if library.steamID, err = findSymbol(handle, "SteamEncryptedAppTicket_GetTicketSteamID"); err != nil {
		_ = library.Close()
		return nil, err
	}
	if library.appID, err = findSymbol(handle, "SteamEncryptedAppTicket_GetTicketAppID"); err != nil {
		_ = library.Close()
		return nil, err
	}
	if library.issueTime, err = findSymbol(handle, "SteamEncryptedAppTicket_GetTicketIssueTime"); err != nil {
		_ = library.Close()
		return nil, err
	}
	if library.vacBanned, err = findSymbol(handle, "SteamEncryptedAppTicket_BUserIsVacBanned"); err != nil {
		_ = library.Close()
		return nil, err
	}
	return library, nil
}

func defaultNativeTicketLibraryPath() string {
	return "/usr/local/lib/libsdkencryptedappticket.so"
}

func findSymbol(handle unsafe.Pointer, name string) (unsafe.Pointer, error) {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	C.dlerror()
	symbol := C.dlsym(handle, cName)
	if symbol == nil {
		return nil, fmt.Errorf("resolve %s: %s", name, C.GoString(C.dlerror()))
	}
	return symbol, nil
}

func (l *linuxTicketLibrary) Decrypt(encryptedTicket []byte, key []byte) ([]byte, error) {
	if len(encryptedTicket) == 0 || len(key) != symmetricKeyBytes {
		return nil, errors.New("invalid decrypt input")
	}
	decrypted := make([]byte, 4096)
	decryptedLength := C.uint32_t(len(decrypted))
	result := C.call_decrypt(
		l.decrypt,
		(*C.uint8_t)(unsafe.Pointer(&encryptedTicket[0])),
		C.uint32_t(len(encryptedTicket)),
		(*C.uint8_t)(unsafe.Pointer(&decrypted[0])),
		&decryptedLength,
		(*C.uint8_t)(unsafe.Pointer(&key[0])),
		C.int(len(key)),
	)
	if result == 0 {
		clear(decrypted)
		return nil, errors.New("SteamEncryptedAppTicket_BDecryptTicket rejected the ticket")
	}
	if decryptedLength == 0 || int(decryptedLength) > len(decrypted) {
		clear(decrypted)
		return nil, errors.New("Steam ticket library returned an invalid plaintext length")
	}
	return decrypted[:decryptedLength], nil
}

func (l *linuxTicketLibrary) SteamID(ticket []byte) (uint64, error) {
	if len(ticket) == 0 {
		return 0, errors.New("empty decrypted ticket")
	}
	var value C.uint64_t
	C.call_steam_id(
		l.steamID,
		(*C.uint8_t)(unsafe.Pointer(&ticket[0])),
		C.uint32_t(len(ticket)),
		&value,
	)
	return uint64(value), nil
}

func (l *linuxTicketLibrary) AppID(ticket []byte) (uint32, error) {
	if len(ticket) == 0 {
		return 0, errors.New("empty decrypted ticket")
	}
	return uint32(C.call_uint32(
		l.appID,
		(*C.uint8_t)(unsafe.Pointer(&ticket[0])),
		C.uint32_t(len(ticket)),
	)), nil
}

func (l *linuxTicketLibrary) IssueTime(ticket []byte) (uint32, error) {
	if len(ticket) == 0 {
		return 0, errors.New("empty decrypted ticket")
	}
	return uint32(C.call_uint32(
		l.issueTime,
		(*C.uint8_t)(unsafe.Pointer(&ticket[0])),
		C.uint32_t(len(ticket)),
	)), nil
}

func (l *linuxTicketLibrary) VACBanned(ticket []byte) (bool, error) {
	if len(ticket) == 0 {
		return false, errors.New("empty decrypted ticket")
	}
	return C.call_bool(
		l.vacBanned,
		(*C.uint8_t)(unsafe.Pointer(&ticket[0])),
		C.uint32_t(len(ticket)),
	) != 0, nil
}

func (l *linuxTicketLibrary) Close() error {
	if l.handle == nil {
		return nil
	}
	if result := C.dlclose(l.handle); result != 0 {
		return fmt.Errorf("dlclose Steam ticket library: %s", C.GoString(C.dlerror()))
	}
	l.handle = nil
	return nil
}

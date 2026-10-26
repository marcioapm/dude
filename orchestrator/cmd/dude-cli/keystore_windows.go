package main

import (
	"errors"
	"syscall"
	"unsafe"
)

// The Windows Credential Manager, a generic credential named dude:HOST.
var osKeystore keystore = winCred{}

var (
	advapi32       = syscall.NewLazyDLL("advapi32.dll")
	procCredRead   = advapi32.NewProc("CredReadW")
	procCredWrite  = advapi32.NewProc("CredWriteW")
	procCredDelete = advapi32.NewProc("CredDeleteW")
	procCredFree   = advapi32.NewProc("CredFree")
)

const (
	credTypeGeneric         = 1
	credPersistLocalMachine = 2
	errorNotFound           = syscall.Errno(1168)
)

// credential is CREDENTIALW.
type credential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        syscall.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

type winCred struct{}

func (winCred) name() string { return "Windows Credential Manager" }

func target(host string) *uint16 {
	p, _ := syscall.UTF16PtrFromString("dude:" + host)
	return p
}

func (winCred) get(host string) (string, error) {
	var c *credential
	r, _, err := procCredRead.Call(uintptr(unsafe.Pointer(target(host))), credTypeGeneric, 0, uintptr(unsafe.Pointer(&c)))
	if r == 0 {
		if errors.Is(err, errorNotFound) {
			return "", errNoKey
		}
		return "", err
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(c)))
	if c.CredentialBlobSize == 0 {
		return "", errNoKey
	}
	return string(unsafe.Slice(c.CredentialBlob, c.CredentialBlobSize)), nil
}

func (winCred) set(host, key string) error {
	blob := []byte(key)
	user, _ := syscall.UTF16PtrFromString("dude")
	c := credential{
		Type:               credTypeGeneric,
		TargetName:         target(host),
		CredentialBlobSize: uint32(len(blob)),
		CredentialBlob:     &blob[0],
		Persist:            credPersistLocalMachine,
		UserName:           user,
	}
	if r, _, err := procCredWrite.Call(uintptr(unsafe.Pointer(&c)), 0); r == 0 {
		return err
	}
	return nil
}

func (winCred) del(host string) error {
	r, _, err := procCredDelete.Call(uintptr(unsafe.Pointer(target(host))), credTypeGeneric, 0)
	if r == 0 {
		if errors.Is(err, errorNotFound) {
			return errNoKey
		}
		return err
	}
	return nil
}

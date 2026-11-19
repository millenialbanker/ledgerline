//go:build windows

package main

// The USB token on Windows, through the Windows certificate store and the
// token driver's CryptoAPI provider, with the standard library's syscall
// package only (no cgo). Token drivers register the token's certificate in
// the user's personal store (CurrentUser\My) with a link to its private
// key; signing goes to the driver, which asks the token to sign.
//
// Drivers come in two kinds, and both are handled:
//   - CNG key storage providers: NCryptSignHash with PKCS #1 padding
//     naming SHA-256 (ECDSA keys: no padding; Windows gives r‖s, turned
//     into DER here).
//   - Legacy CryptoAPI (CAPI) providers, which many Indian token drivers
//     are: CryptCreateHash(CALG_SHA_256), CryptSetHashParam(HP_HASHVAL)
//     with the digest Ledgerline sent, CryptSignHash. CAPI gives the
//     signature little-endian; it is reversed here.
//
// The PIN is set on the key once (NCRYPT_PIN_PROPERTY or
// PP_SIGNATURE_PIN / PP_KEYEXCHANGE_PIN) so the driver need not ask for
// it for every document. Some drivers show their own PIN window anyway.
//
// This file cannot be run on the build machine; it needs testing on a real
// token (see the -selftest flag).

import (
	"bufio"
	"crypto"
	"crypto/ecdsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

const tokenSupported = true

// Win32 constants used (values from the Windows SDK headers wincrypt.h,
// ncrypt.h, bcrypt.h, dpapi.h, wincon.h, libloaderapi.h).
const (
	certStoreProvSystemW       = 10             // CERT_STORE_PROV_SYSTEM_W
	certSystemStoreCurrentUser = 0x00010000     // CERT_SYSTEM_STORE_CURRENT_USER (1 << 16)
	certStoreOpenExistingFlag  = 0x00004000     // CERT_STORE_OPEN_EXISTING_FLAG
	certStoreReadonlyFlag      = 0x00008000     // CERT_STORE_READONLY_FLAG
	certKeyProvInfoPropID      = 2              // CERT_KEY_PROV_INFO_PROP_ID
	cryptAcquireAllowNCryptKey = 0x00010000     // CRYPT_ACQUIRE_ALLOW_NCRYPT_KEY_FLAG
	certNCryptKeySpec          = 0xFFFFFFFF     // CERT_NCRYPT_KEY_SPEC: the handle is an NCRYPT_KEY_HANDLE
	atKeyExchange              = 1              // AT_KEYEXCHANGE
	atSignature                = 2              // AT_SIGNATURE
	bcryptPadPKCS1             = 0x00000002     // BCRYPT_PAD_PKCS1
	calgSHA256                 = 0x0000800c     // CALG_SHA_256
	hpHashVal                  = 0x0002         // HP_HASHVAL
	ppKeyExchangePIN           = 32             // PP_KEYEXCHANGE_PIN
	ppSignaturePIN             = 33             // PP_SIGNATURE_PIN
	cryptProtectUIForbidden    = 0x1            // CRYPTPROTECT_UI_FORBIDDEN
	enableEchoInput            = 0x0004         // ENABLE_ECHO_INPUT
	loadLibrarySearchSystem32  = 0x00000800     // LOAD_LIBRARY_SEARCH_SYSTEM32
	ncryptPINProperty          = "SmartCardPin" // NCRYPT_PIN_PROPERTY
	ncryptSHA256Algorithm      = "SHA256"       // BCRYPT_SHA256_ALGORITHM
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	crypt32  = syscall.NewLazyDLL("crypt32.dll")
	ncrypt   = syscall.NewLazyDLL("ncrypt.dll")
	advapi32 = syscall.NewLazyDLL("advapi32.dll")

	procSetDefaultDllDirectories = kernel32.NewProc("SetDefaultDllDirectories")
	procSetConsoleMode           = kernel32.NewProc("SetConsoleMode")
	procLocalFree                = kernel32.NewProc("LocalFree")

	procCertOpenStore                     = crypt32.NewProc("CertOpenStore")
	procCertCloseStore                    = crypt32.NewProc("CertCloseStore")
	procCertEnumCertificatesInStore       = crypt32.NewProc("CertEnumCertificatesInStore")
	procCertDuplicateCertificateContext   = crypt32.NewProc("CertDuplicateCertificateContext")
	procCertFreeCertificateContext        = crypt32.NewProc("CertFreeCertificateContext")
	procCertGetCertificateContextProperty = crypt32.NewProc("CertGetCertificateContextProperty")
	procCryptAcquireCertificatePrivateKey = crypt32.NewProc("CryptAcquireCertificatePrivateKey")
	procCryptProtectData                  = crypt32.NewProc("CryptProtectData")
	procCryptUnprotectData                = crypt32.NewProc("CryptUnprotectData")

	procNCryptSignHash    = ncrypt.NewProc("NCryptSignHash")
	procNCryptSetProperty = ncrypt.NewProc("NCryptSetProperty")
	procNCryptFreeObject  = ncrypt.NewProc("NCryptFreeObject")

	procCryptCreateHash     = advapi32.NewProc("CryptCreateHash")
	procCryptSetHashParam   = advapi32.NewProc("CryptSetHashParam")
	procCryptSignHashW      = advapi32.NewProc("CryptSignHashW")
	procCryptDestroyHash    = advapi32.NewProc("CryptDestroyHash")
	procCryptSetProvParam   = advapi32.NewProc("CryptSetProvParam")
	procCryptReleaseContext = advapi32.NewProc("CryptReleaseContext")
)

func init() {
	// SetDefaultDllDirectories(LOAD_LIBRARY_SEARCH_SYSTEM32): the DLLs
	// above load from System32 only, never from the folder the program
	// was downloaded to. (kernel32 is always loaded already.)
	if procSetDefaultDllDirectories.Find() == nil {
		procSetDefaultDllDirectories.Call(loadLibrarySearchSystem32)
	}
}

// certContext is CERT_CONTEXT (wincrypt.h).
type certContext struct {
	EncodingType uint32
	Encoded      *byte
	Length       uint32
	CertInfo     uintptr
	Store        uintptr
}

// certCtx reads the CERT_CONTEXT at an address Windows returned. The
// memory is Windows'; Go's collector neither moves nor frees it.
func certCtx(p uintptr) *certContext {
	return (*certContext)(*(*unsafe.Pointer)(unsafe.Pointer(&p)))
}

// winErr describes a failed call: the function and the Windows error code.
func winErr(fn string, e error) error {
	if no, ok := e.(syscall.Errno); ok && no != 0 {
		return fmt.Errorf("%s: %s (0x%08X)", fn, strings.TrimSpace(no.Error()), uint32(no))
	}
	return fmt.Errorf("%s failed", fn)
}

// openMyStore opens CurrentUser\My read-only:
// CertOpenStore(CERT_STORE_PROV_SYSTEM_W, 0, 0, CERT_SYSTEM_STORE_CURRENT_USER | OPEN_EXISTING | READONLY, L"MY").
func openMyStore() (uintptr, error) {
	my, _ := syscall.UTF16PtrFromString("MY")
	h, _, e := procCertOpenStore.Call(certStoreProvSystemW, 0, 0, certSystemStoreCurrentUser|certStoreOpenExistingFlag|certStoreReadonlyFlag, uintptr(unsafe.Pointer(my)))
	if h == 0 {
		return 0, winErr("CertOpenStore", e)
	}
	return h, nil
}

type storeCert struct {
	cert  *x509.Certificate
	thumb string // SHA-1 of the certificate, hex (Windows' "thumbprint")
}

// eachCert calls fn for each certificate in CurrentUser\My that has a
// private key (CERT_KEY_PROV_INFO_PROP_ID is set), until fn returns true.
// fn gets the CERT_CONTEXT, valid only during the call.
func eachCert(fn func(ctx uintptr, c storeCert) bool) error {
	st, err := openMyStore()
	if err != nil {
		return err
	}
	defer procCertCloseStore.Call(st, 0)
	var prev uintptr
	for {
		// CertEnumCertificatesInStore frees prev and returns the next, or 0.
		cur, _, _ := procCertEnumCertificatesInStore.Call(st, prev)
		if cur == 0 {
			return nil
		}
		prev = cur
		var n uint32
		if ok, _, _ := procCertGetCertificateContextProperty.Call(cur, certKeyProvInfoPropID, 0, uintptr(unsafe.Pointer(&n))); ok == 0 {
			continue // no private key linked
		}
		cc := certCtx(cur)
		der := append([]byte(nil), unsafe.Slice(cc.Encoded, cc.Length)...)
		c, err := x509.ParseCertificate(der)
		if err != nil {
			continue
		}
		sum := sha1.Sum(der)
		if fn(cur, storeCert{cert: c, thumb: strings.ToUpper(hex.EncodeToString(sum[:]))}) {
			procCertFreeCertificateContext.Call(cur) // stopped early: free the current one
			return nil
		}
	}
}

// chooseCert lists the certificates with a private key and asks which to
// sign with; it returns its thumbprint.
func chooseCert(in *bufio.Reader) (string, error) {
	var list []storeCert
	if err := eachCert(func(_ uintptr, c storeCert) bool { list = append(list, c); return false }); err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", errors.New("no certificate with a private key found: plug in the USB token, wait for its software to show the certificate, and try again")
	}
	fmt.Println("Certificates on this computer with a private key:")
	now := time.Now()
	for i, s := range list {
		note := ""
		switch {
		case now.After(s.cert.NotAfter):
			note = "  (expired)"
		case now.Before(s.cert.NotBefore):
			note = "  (not valid yet)"
		case s.cert.KeyUsage != 0 && s.cert.KeyUsage&(x509.KeyUsageDigitalSignature|x509.KeyUsageContentCommitment) == 0:
			note = "  (not for signing)"
		}
		fmt.Printf("  %d. %s, issued by %s, valid until %s, serial %X%s\n", i+1, firstOf(s.cert.Subject.CommonName, s.cert.Subject.String()),
			firstOf(s.cert.Issuer.CommonName, s.cert.Issuer.String()), s.cert.NotAfter.Format("02-Jan-2006"), s.cert.SerialNumber, note)
	}
	fmt.Print("Number of the firm's signing certificate: ")
	line, err := readLine(in)
	if err != nil {
		return "", err
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(list) {
		return "", errors.New("give one of the numbers shown")
	}
	return list[n-1].thumb, nil
}

// tokenBackend signs with the token's key.
type tokenBackend struct {
	thumb, pin string // the PIN is kept in memory only, for Reopen
	ctx        uintptr
	cert       *x509.Certificate
	key        uintptr // NCRYPT_KEY_HANDLE, or HCRYPTPROV for CAPI
	spec       uint32  // CERT_NCRYPT_KEY_SPEC, AT_SIGNATURE or AT_KEYEXCHANGE
	mustFree   bool
}

func openToken(thumb, pin string) (backend, error) {
	t := &tokenBackend{thumb: strings.ToUpper(thumb), pin: pin}
	if err := t.open(); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *tokenBackend) open() error {
	var found uintptr
	var cert *x509.Certificate
	if err := eachCert(func(cur uintptr, c storeCert) bool {
		if c.thumb != t.thumb {
			return false
		}
		// Keep our own reference after the enumeration ends.
		found, _, _ = procCertDuplicateCertificateContext.Call(cur)
		cert = c.cert
		return true
	}); err != nil {
		return err
	}
	if found == 0 {
		return errors.New("the chosen certificate is not on this computer now: plug in the USB token (or run ledgerline-signer choose)")
	}
	// CryptAcquireCertificatePrivateKey(pCert, CRYPT_ACQUIRE_ALLOW_NCRYPT_KEY_FLAG, NULL, &key, &keySpec, &callerFree).
	var key uintptr
	var spec uint32
	var callerFree int32
	r, _, e := procCryptAcquireCertificatePrivateKey.Call(found, cryptAcquireAllowNCryptKey, 0,
		uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&spec)), uintptr(unsafe.Pointer(&callerFree)))
	if r == 0 {
		procCertFreeCertificateContext.Call(found)
		return winErr("CryptAcquireCertificatePrivateKey (is the token plugged in?)", e)
	}
	t.ctx, t.cert, t.key, t.spec, t.mustFree = found, cert, key, spec, callerFree != 0
	t.setPIN()
	return nil
}

// setPIN gives the driver the PIN once. Failures are ignored: the driver
// then asks with its own window.
func (t *tokenBackend) setPIN() {
	if t.pin == "" {
		return
	}
	if t.spec == certNCryptKeySpec {
		// NCryptSetProperty(key, NCRYPT_PIN_PROPERTY, PIN as UTF-16 with its NUL, size in bytes, 0).
		name, _ := syscall.UTF16PtrFromString(ncryptPINProperty)
		w := utf16.Encode([]rune(t.pin + "\x00"))
		procNCryptSetProperty.Call(t.key, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&w[0])), uintptr(len(w)*2), 0)
		clear(w)
		return
	}
	// CryptSetProvParam(prov, PP_SIGNATURE_PIN / PP_KEYEXCHANGE_PIN, PIN as a NUL-ended string, 0).
	a := append([]byte(t.pin), 0)
	procCryptSetProvParam.Call(t.key, ppSignaturePIN, uintptr(unsafe.Pointer(&a[0])), 0)
	if t.spec == atKeyExchange {
		procCryptSetProvParam.Call(t.key, ppKeyExchangePIN, uintptr(unsafe.Pointer(&a[0])), 0)
	}
	clear(a)
}

func (t *tokenBackend) Public() crypto.PublicKey       { return t.cert.PublicKey }
func (t *tokenBackend) Certificate() *x509.Certificate { return t.cert }

func (t *tokenBackend) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	if opts == nil || opts.HashFunc() != crypto.SHA256 || len(digest) != sha256.Size {
		return nil, errors.New("only SHA-256 digests are signed")
	}
	if t.key == 0 {
		return nil, errors.New("the token is not open")
	}
	_, ec := t.cert.PublicKey.(*ecdsa.PublicKey)
	if t.spec == certNCryptKeySpec {
		sig, err := t.signCNG(digest, ec)
		if err != nil || !ec {
			return sig, err
		}
		return ecdsaRawToDER(sig)
	}
	if ec {
		return nil, errors.New("an ECDSA key on a CryptoAPI (CAPI) provider cannot sign; the token needs a CNG driver")
	}
	return t.signCAPI(digest)
}

// bcryptPKCS1PaddingInfo is BCRYPT_PKCS1_PADDING_INFO (bcrypt.h).
type bcryptPKCS1PaddingInfo struct {
	AlgID *uint16
}

// signCNG: NCryptSignHash(key, &padding, digest, 32, sig, len, &len, BCRYPT_PAD_PKCS1),
// first with no buffer to learn the size. ECDSA: no padding, flags 0.
func (t *tokenBackend) signCNG(digest []byte, ec bool) ([]byte, error) {
	var pad *bcryptPKCS1PaddingInfo
	var flags uintptr
	if !ec {
		alg, _ := syscall.UTF16PtrFromString(ncryptSHA256Algorithm)
		pad, flags = &bcryptPKCS1PaddingInfo{AlgID: alg}, bcryptPadPKCS1
	}
	var n uint32
	st, _, _ := procNCryptSignHash.Call(t.key, uintptr(unsafe.Pointer(pad)), uintptr(unsafe.Pointer(&digest[0])), uintptr(len(digest)), 0, 0, uintptr(unsafe.Pointer(&n)), flags)
	if st != 0 || n == 0 {
		return nil, fmt.Errorf("NCryptSignHash: error 0x%08X", uint32(st))
	}
	sig := make([]byte, n)
	st, _, _ = procNCryptSignHash.Call(t.key, uintptr(unsafe.Pointer(pad)), uintptr(unsafe.Pointer(&digest[0])), uintptr(len(digest)),
		uintptr(unsafe.Pointer(&sig[0])), uintptr(n), uintptr(unsafe.Pointer(&n)), flags)
	runtime.KeepAlive(pad)
	if st != 0 {
		return nil, fmt.Errorf("NCryptSignHash: error 0x%08X", uint32(st))
	}
	return sig[:n], nil
}

// signCAPI: CryptCreateHash(prov, CALG_SHA_256), CryptSetHashParam(HP_HASHVAL,
// digest), CryptSignHashW(hash, keySpec, NULL, 0, sig, &len), reversed
// from little-endian.
func (t *tokenBackend) signCAPI(digest []byte) ([]byte, error) {
	var h uintptr
	if r, _, e := procCryptCreateHash.Call(t.key, calgSHA256, 0, 0, uintptr(unsafe.Pointer(&h))); r == 0 {
		return nil, winErr("CryptCreateHash(CALG_SHA_256) (the token's driver may not offer SHA-256)", e)
	}
	defer procCryptDestroyHash.Call(h)
	if r, _, e := procCryptSetHashParam.Call(h, hpHashVal, uintptr(unsafe.Pointer(&digest[0])), 0); r == 0 {
		return nil, winErr("CryptSetHashParam(HP_HASHVAL)", e)
	}
	var n uint32
	if r, _, e := procCryptSignHashW.Call(h, uintptr(t.spec), 0, 0, 0, uintptr(unsafe.Pointer(&n))); r == 0 || n == 0 {
		return nil, winErr("CryptSignHash", e)
	}
	sig := make([]byte, n)
	if r, _, e := procCryptSignHashW.Call(h, uintptr(t.spec), 0, 0, uintptr(unsafe.Pointer(&sig[0])), uintptr(unsafe.Pointer(&n))); r == 0 {
		return nil, winErr("CryptSignHash", e)
	}
	sig = sig[:n]
	for i, j := 0, len(sig)-1; i < j; i, j = i+1, j-1 {
		sig[i], sig[j] = sig[j], sig[i]
	}
	return sig, nil
}

// Reopen gets the key again (after the token was pulled out and put back)
// and signs a sample to be sure it works.
func (t *tokenBackend) Reopen() error {
	t.Close()
	if err := t.open(); err != nil {
		return err
	}
	sample := sha256.Sum256([]byte("Ledgerline Signer token check"))
	sig, err := t.Sign(nil, sample[:], crypto.SHA256)
	if err == nil {
		err = verifyRaw(t.cert, sample[:], sig)
	}
	if err != nil {
		t.Close()
	}
	return err
}

// Close frees the key (NCryptFreeObject or CryptReleaseContext, when
// Windows says the caller must) and the certificate context.
func (t *tokenBackend) Close() {
	if t.key != 0 && t.mustFree {
		if t.spec == certNCryptKeySpec {
			procNCryptFreeObject.Call(t.key)
		} else {
			procCryptReleaseContext.Call(t.key, 0)
		}
	}
	if t.ctx != 0 {
		procCertFreeCertificateContext.Call(t.ctx)
	}
	t.key, t.ctx = 0, 0
}

// ---- DPAPI: the agent key, readable only by this Windows user ----

// dataBlob is DATA_BLOB (dpapi.h).
type dataBlob struct {
	Size uint32
	Data *byte
}

// protect: CryptProtectData(&in, description, NULL, NULL, NULL, CRYPTPROTECT_UI_FORBIDDEN, &out).
func protect(b []byte) (string, error) {
	if len(b) == 0 {
		return "", errors.New("nothing to protect")
	}
	in := dataBlob{Size: uint32(len(b)), Data: &b[0]}
	var out dataBlob
	descr, _ := syscall.UTF16PtrFromString("Ledgerline Signer agent key")
	r, _, e := procCryptProtectData.Call(uintptr(unsafe.Pointer(&in)), uintptr(unsafe.Pointer(descr)), 0, 0, 0, cryptProtectUIForbidden, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return "", winErr("CryptProtectData", e)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.Data)))
	return base64.StdEncoding.EncodeToString(unsafe.Slice(out.Data, out.Size)), nil
}

// unprotect: CryptUnprotectData(&in, NULL, NULL, NULL, NULL, CRYPTPROTECT_UI_FORBIDDEN, &out).
func unprotect(s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) == 0 {
		return nil, errors.New("the saved agent key is unreadable")
	}
	in := dataBlob{Size: uint32(len(b)), Data: &b[0]}
	var out dataBlob
	r, _, e := procCryptUnprotectData.Call(uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, cryptProtectUIForbidden, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return nil, winErr("CryptUnprotectData (was it set up by another Windows user?)", e)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

// readSecret asks for the PIN or a password with the console's echo off
// (SetConsoleMode without ENABLE_ECHO_INPUT), and puts the mode back.
func readSecret(prompt string, in *bufio.Reader) (string, error) {
	fmt.Print(prompt)
	h := syscall.Handle(os.Stdin.Fd())
	var mode uint32
	hidden := false
	if syscall.GetConsoleMode(h, &mode) == nil {
		r, _, _ := procSetConsoleMode.Call(uintptr(h), uintptr(mode&^enableEchoInput))
		hidden = r != 0
	}
	s, err := readLine(in)
	if hidden {
		procSetConsoleMode.Call(uintptr(h), uintptr(mode))
		fmt.Println()
	}
	return s, err
}

var _ = atSignature // the usual key spec; any value Windows returns is passed back to CryptSignHash

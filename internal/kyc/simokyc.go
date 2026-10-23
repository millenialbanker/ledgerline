package kyc

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"hash/crc32"
	"math/big"
	"strings"
	"time"
)

// practiceSigner signs practice offline e-KYC files in simulate mode. Its
// certificate is trusted only by a simulating server.
type practiceSigner struct {
	key  *rsa.PrivateKey
	cert *x509.Certificate
}

func newPracticeSigner() (*practiceSigner, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Practice UIDAI offline e-KYC (not real)"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	c, err := x509.ParseCertificate(der)
	return &practiceSigner{key, c}, err
}

// practiceZip makes an offline e-KYC ZIP for the investor's own details, as
// UIDAI would: signed XML with hashed mobile and email, encrypted with the
// share code.
func (p *practiceSigner) practiceZip(h Hint, shareCode string, at time.Time) ([]byte, error) {
	last4 := lastDigits(h.PAN + h.Name)
	ref := last4 + at.In(ist).Format("20060102150405") + fmt.Sprintf("%03d", at.Nanosecond()/1e6)
	ld := int(last4[3] - '0')
	dob := ""
	if t, err := time.Parse("2006-01-02", h.DOB); err == nil {
		dob = t.Format("02-01-2006")
	}
	m, e := "", ""
	if h.Mobile != "" {
		m = offlineHash(mobile10(h.Mobile), shareCode, ld)
	}
	if h.Email != "" {
		e = offlineHash(strings.TrimSpace(h.Email), shareCode, ld)
	}
	body := fmt.Sprintf(`<OfflinePaperlessKyc referenceId="%s"><UidData><Poi dob="%s" e="%s" gender="F" m="%s" name="%s"></Poi><Poa careof="" country="India" dist="Bengaluru Urban" house="12" landmark="Near Metro Station" loc="Indiranagar" pc="560038" po="Indiranagar" state="Karnataka" street="4th Cross" subdist="Bengaluru East" vtc="Bengaluru"></Poa><Pht>%s</Pht></UidData>`,
		ref, dob, e, m, escAttr(h.Name), base64.StdEncoding.EncodeToString(simPhoto()))
	// The unsigned document is already canonical (attributes sorted, end tags written out).
	dg := sha256.Sum256([]byte(body + "</OfflinePaperlessKyc>"))
	si := `<SignedInfo xmlns="` + dsigNS + `"><CanonicalizationMethod Algorithm="` + c14nAlg + `"></CanonicalizationMethod><SignatureMethod Algorithm="` + dsigNS + `rsa-sha1"></SignatureMethod><Reference URI=""><Transforms><Transform Algorithm="` + envelopAlg + `"></Transform></Transforms><DigestMethod Algorithm="http://www.w3.org/2001/04/xmlenc#sha256"></DigestMethod><DigestValue>` + base64.StdEncoding.EncodeToString(dg[:]) + `</DigestValue></Reference></SignedInfo>`
	sh := sha1.Sum([]byte(si))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA1, sh[:])
	if err != nil {
		return nil, err
	}
	doc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" + body +
		`<Signature xmlns="` + dsigNS + `">` + strings.Replace(si, ` xmlns="`+dsigNS+`"`, "", 1) +
		`<SignatureValue>` + base64.StdEncoding.EncodeToString(sig) + `</SignatureValue></Signature></OfflinePaperlessKyc>`
	return zipCryptoArchive("offlineaadhaar"+ref+".xml", []byte(doc), shareCode, at)
}

// zipCryptoArchive writes a one-file ZIP with traditional PKWARE encryption,
// as UIDAI's downloads are.
func zipCryptoArchive(name string, data []byte, pw string, at time.Time) ([]byte, error) {
	var comp bytes.Buffer
	fw, _ := flate.NewWriter(&comp, flate.BestCompression)
	fw.Write(data)
	fw.Close()
	crc := crc32.ChecksumIEEE(data)
	k0, k1, k2 := uint32(0x12345678), uint32(0x23456789), uint32(0x34567890)
	tab := crc32.IEEETable
	update := func(c byte) {
		k0 = tab[byte(k0)^c] ^ (k0 >> 8)
		k1 = (k1+(k0&0xff))*134775813 + 1
		k2 = tab[byte(k2)^byte(k1>>24)] ^ (k2 >> 8)
	}
	for i := 0; i < len(pw); i++ {
		update(pw[i])
	}
	head := make([]byte, 12)
	_, _ = rand.Read(head[:11])
	head[11] = byte(crc >> 24)
	plain := append(head, comp.Bytes()...)
	enc := make([]byte, len(plain))
	for i, p := range plain {
		t := uint16(k2 | 2)
		enc[i] = p ^ byte((t*(t^1))>>8)
		update(p)
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	w, err := zw.CreateRaw(&zip.FileHeader{Name: name, Method: zip.Deflate, Flags: 1, CRC32: crc,
		CompressedSize64: uint64(len(enc)), UncompressedSize64: uint64(len(data)), Modified: at})
	if err != nil {
		return nil, err
	}
	w.Write(enc)
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

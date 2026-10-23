package kyc

// Aadhaar Paperless Offline e-KYC: for investors who do not use DigiLocker.
//
// The investor downloads a ZIP from UIDAI (myAadhaar or the mAadhaar app,
// with an OTP to their Aadhaar-linked mobile), protected with a 4-digit
// share code they choose. Inside is an XML signed by UIDAI with name, date
// of birth, gender, address and photograph, the last four digits of the
// Aadhaar number with the download time, and hashes of the linked mobile
// and email. SEBI accepts it for KYC without in-person verification when
// it is not older than 3 days (SEBI/HO/MIRSD/DOP/CIR/P/2020/73, para 4).
//
// Verification here: open the ZIP with the share code (ZipCrypto or AES),
// check UIDAI's XML signature against UIDAI's published certificate (the
// key is never taken from the file), refuse files older than 3 days, and
// compare the mobile and email the investor confirmed with the hashes.

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto"
	"crypto/aes"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// OfflineMaxAge is how old an offline e-KYC file may be (SEBI: 3 days).
const OfflineMaxAge = 72 * time.Hour

const maxOfflineSize = 5 << 20

// Offline e-KYC errors the investor can act on.
var (
	ErrShareCode    = errors.New("the share code does not open this file")
	ErrNotOffline   = errors.New("this is not an Aadhaar offline e-KYC file from UIDAI")
	ErrBadSignature = errors.New("UIDAI's signature on this file is not valid: it was changed after download, or is not from UIDAI")
)

// ---- the ZIP ----

// OpenOfflineZip returns the XML inside an offline e-KYC ZIP.
func OpenOfflineZip(data []byte, shareCode string) ([]byte, error) {
	if len(data) > maxOfflineSize {
		return nil, errors.New("the file is too large to be an Aadhaar offline e-KYC ZIP")
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("this is not a ZIP file: upload the ZIP downloaded from myAadhaar, not its contents")
	}
	var f *zip.File
	for _, x := range zr.File {
		if strings.HasSuffix(strings.ToLower(x.Name), ".xml") {
			if f != nil {
				return nil, ErrNotOffline
			}
			f = x
		}
	}
	if f == nil || len(zr.File) > 2 {
		return nil, ErrNotOffline
	}
	if f.UncompressedSize64 > maxOfflineSize {
		return nil, ErrNotOffline
	}
	raw, err := f.OpenRaw()
	if err != nil {
		return nil, ErrNotOffline
	}
	enc, err := io.ReadAll(io.LimitReader(raw, maxOfflineSize+1))
	if err != nil {
		return nil, ErrNotOffline
	}
	method, checkCRC := f.Method, true
	var plain []byte
	switch {
	case f.Method == 99:
		ae, err := aesExtra(f.Extra)
		if err != nil {
			return nil, err
		}
		if plain, err = decryptAES(enc, shareCode, ae.strength); err != nil {
			return nil, err
		}
		method, checkCRC = ae.method, ae.version == 1
	case f.Flags&1 == 1:
		check := byte(f.CRC32 >> 24)
		if f.Flags&8 != 0 {
			check = byte(f.ModifiedTime >> 8)
		}
		if plain, err = decryptZipCrypto(enc, shareCode, check); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("this ZIP has no share code: download the file again from myAadhaar")
	}
	var out []byte
	switch method {
	case zip.Store:
		out = plain
	case zip.Deflate:
		out, err = io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(plain)), maxOfflineSize+1))
		if err != nil {
			return nil, ErrShareCode // a wrong code that passed the one-byte check
		}
	default:
		return nil, ErrNotOffline
	}
	if len(out) > maxOfflineSize {
		return nil, ErrNotOffline
	}
	if checkCRC && crc32.ChecksumIEEE(out) != f.CRC32 {
		return nil, ErrShareCode
	}
	return out, nil
}

// decryptZipCrypto is PKWARE's traditional encryption (APPNOTE 6.1).
func decryptZipCrypto(enc []byte, pw string, check byte) ([]byte, error) {
	if len(enc) < 12 {
		return nil, ErrNotOffline
	}
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
	out := make([]byte, len(enc))
	for i, c := range enc {
		t := uint16(k2 | 2)
		p := c ^ byte((t*(t^1))>>8)
		update(p)
		out[i] = p
	}
	if out[11] != check {
		return nil, ErrShareCode
	}
	return out[12:], nil
}

type aeInfo struct {
	version  uint16
	strength byte
	method   uint16
}

func aesExtra(extra []byte) (aeInfo, error) {
	for len(extra) >= 4 {
		id, n := binary.LittleEndian.Uint16(extra), int(binary.LittleEndian.Uint16(extra[2:]))
		if 4+n > len(extra) {
			break
		}
		if id == 0x9901 && n >= 7 && string(extra[6:8]) == "AE" {
			d := extra[4:]
			return aeInfo{binary.LittleEndian.Uint16(d), d[4], binary.LittleEndian.Uint16(d[5:])}, nil
		}
		extra = extra[4+n:]
	}
	return aeInfo{}, ErrNotOffline
}

// decryptAES is WinZip AES (AE-1/AE-2): PBKDF2-HMAC-SHA1 with 1000
// rounds, AES-CTR with a little-endian counter from 1, HMAC-SHA1 tag.
func decryptAES(enc []byte, pw string, strength byte) ([]byte, error) {
	kl := map[byte]int{1: 16, 2: 24, 3: 32}[strength]
	if kl == 0 {
		return nil, ErrNotOffline
	}
	sl := kl / 2
	if len(enc) < sl+2+10 {
		return nil, ErrNotOffline
	}
	salt, verify, body, tag := enc[:sl], enc[sl:sl+2], enc[sl+2:len(enc)-10], enc[len(enc)-10:]
	km, err := pbkdf2.Key(sha1.New, pw, salt, 1000, 2*kl+2)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(km[2*kl:], verify) != 1 {
		return nil, ErrShareCode
	}
	m := hmac.New(sha1.New, km[kl:2*kl])
	m.Write(body)
	if !hmac.Equal(m.Sum(nil)[:10], tag) {
		return nil, ErrShareCode
	}
	blk, err := aes.NewCipher(km[:kl])
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(body))
	var ctr, ks [16]byte
	for i := 0; i < len(body); i += 16 {
		for j := 0; j < 16; j++ { // increment the little-endian counter
			ctr[j]++
			if ctr[j] != 0 {
				break
			}
		}
		blk.Encrypt(ks[:], ctr[:])
		for j := 0; j < 16 && i+j < len(body); j++ {
			out[i+j] = body[i+j] ^ ks[j]
		}
	}
	return out, nil
}

// ---- UIDAI's certificate ----

// LoadCerts reads certificates (PEM or DER) from files.
func LoadCerts(paths []string) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		cs, err := ParseCerts(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, cs...)
	}
	return out, nil
}

// ParseCerts reads PEM (one or more) or DER, or base64 DER as UIDAI sometimes
// publishes it.
func ParseCerts(b []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	rest := b
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if len(out) > 0 {
		return out, nil
	}
	if c, err := x509.ParseCertificate(b); err == nil {
		return []*x509.Certificate{c}, nil
	}
	if d, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(b)), "")); err == nil {
		if c, err := x509.ParseCertificate(d); err == nil {
			return []*x509.Certificate{c}, nil
		}
	}
	return nil, errors.New("not a certificate (PEM or DER)")
}

// ---- the XML ----

// node is a minimal XML tree, kept as written (prefixes, attribute order),
// for canonicalisation.
type node struct {
	name     string // as written, with prefix
	attrs    []xml.Attr
	kids     []*node // elements and text, in order
	text     string  // for text nodes
	isText   bool
	parent   *node
	procInst *xml.ProcInst
}

func parseTree(b []byte) (*node, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = true
	doc := &node{}
	cur := doc
	for {
		tok, err := dec.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, ErrNotOffline
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{name: qname(t.Name), attrs: append([]xml.Attr(nil), t.Attr...), parent: cur}
			cur.kids = append(cur.kids, n)
			cur = n
		case xml.EndElement:
			if cur.parent == nil || cur.name != qname(t.Name) {
				return nil, ErrNotOffline
			}
			cur = cur.parent
		case xml.CharData:
			if cur != doc {
				cur.kids = append(cur.kids, &node{isText: true, text: string(t), parent: cur})
			}
		case xml.ProcInst:
			if t.Target != "xml" {
				pi := t.Copy()
				cur.kids = append(cur.kids, &node{procInst: &pi, parent: cur})
			}
		case xml.Directive:
			return nil, ErrNotOffline // no DOCTYPE or entities
		}
	}
	if cur != doc {
		return nil, ErrNotOffline
	}
	return doc, nil
}

func qname(n xml.Name) string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

func (n *node) root() *node {
	for _, k := range n.kids {
		if !k.isText && k.procInst == nil {
			return k
		}
	}
	return nil
}

func (n *node) local() string {
	if i := strings.IndexByte(n.name, ':'); i >= 0 {
		return n.name[i+1:]
	}
	return n.name
}

func (n *node) attr(name string) string {
	for _, a := range n.attrs {
		if qname(a.Name) == name {
			return a.Value
		}
	}
	return ""
}

func (n *node) child(local string) *node {
	for _, k := range n.kids {
		if !k.isText && k.procInst == nil && k.local() == local {
			return k
		}
	}
	return nil
}

func (n *node) textContent() string {
	var b strings.Builder
	for _, k := range n.kids {
		if k.isText {
			b.WriteString(k.text)
		} else if k.procInst == nil {
			b.WriteString(k.textContent())
		}
	}
	return b.String()
}

// nsScope returns the namespace declarations in scope at n (from n and its
// ancestors): prefix ("" for default) -> URI.
func (n *node) nsScope() map[string]string {
	var chain []*node
	for x := n; x != nil && x.name != ""; x = x.parent {
		chain = append(chain, x)
	}
	m := map[string]string{}
	for i := len(chain) - 1; i >= 0; i-- {
		for _, a := range chain[i].attrs {
			if a.Name.Space == "" && a.Name.Local == "xmlns" {
				m[""] = a.Value
			} else if a.Name.Space == "xmlns" {
				m[a.Name.Local] = a.Value
			}
		}
	}
	return m
}

// c14n is Canonical XML 1.0 without comments (REC-xml-c14n-20010315) of
// the subtree at n, leaving out skip (for the enveloped-signature
// transform).
func c14n(n *node, skip *node) []byte {
	var b bytes.Buffer
	var walk func(x *node, rendered map[string]string)
	walk = func(x *node, rendered map[string]string) {
		if x == skip {
			return
		}
		switch {
		case x.isText:
			b.WriteString(escText(x.text))
			return
		case x.procInst != nil:
			b.WriteString("<?" + x.procInst.Target)
			if len(x.procInst.Inst) > 0 {
				b.WriteString(" " + string(x.procInst.Inst))
			}
			b.WriteString("?>")
			return
		}
		scope := x.nsScope()
		var nsKeys []string
		for p, u := range scope {
			if r, ok := rendered[p]; ok && r == u {
				continue
			}
			if p == "" && u == "" && rendered[""] == "" {
				continue
			}
			nsKeys = append(nsKeys, p)
		}
		sort.Strings(nsKeys) // "" (the default) sorts first
		b.WriteString("<" + x.name)
		next := map[string]string{}
		for k, v := range rendered {
			next[k] = v
		}
		for _, p := range nsKeys {
			if p == "" {
				b.WriteString(` xmlns="` + escAttr(scope[p]) + `"`)
			} else {
				b.WriteString(" xmlns:" + p + `="` + escAttr(scope[p]) + `"`)
			}
			next[p] = scope[p]
		}
		type at struct{ uri, local, q, v string }
		var as []at
		for _, a := range x.attrs {
			if (a.Name.Space == "" && a.Name.Local == "xmlns") || a.Name.Space == "xmlns" {
				continue
			}
			uri := ""
			if a.Name.Space != "" {
				uri = scope[a.Name.Space]
				if a.Name.Space == "xml" {
					uri = "http://www.w3.org/XML/1998/namespace"
				}
			}
			as = append(as, at{uri, a.Name.Local, qname(a.Name), a.Value})
		}
		sort.Slice(as, func(i, j int) bool {
			if as[i].uri != as[j].uri {
				return as[i].uri < as[j].uri
			}
			return as[i].local < as[j].local
		})
		for _, a := range as {
			b.WriteString(" " + a.q + `="` + escAttr(a.v) + `"`)
		}
		b.WriteString(">")
		for _, k := range x.kids {
			walk(k, next)
		}
		b.WriteString("</" + x.name + ">")
	}
	if n.name == "" { // the document: its root and top-level processing instructions
		first := true
		for _, k := range n.kids {
			if k.isText {
				continue
			}
			if !first {
				b.WriteString("\n")
			}
			walk(k, map[string]string{})
			first = false
		}
		return b.Bytes()
	}
	walk(n, map[string]string{})
	return b.Bytes()
}

var textEsc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\r", "&#xD;")
var attrEsc = strings.NewReplacer("&", "&amp;", "<", "&lt;", `"`, "&quot;", "\t", "&#x9;", "\n", "&#xA;", "\r", "&#xD;")

func escText(s string) string { return textEsc.Replace(s) }
func escAttr(s string) string { return attrEsc.Replace(s) }

const (
	dsigNS     = "http://www.w3.org/2000/09/xmldsig#"
	c14nAlg    = "http://www.w3.org/TR/2001/REC-xml-c14n-20010315"
	envelopAlg = "http://www.w3.org/2000/09/xmldsig#enveloped-signature"
)

// VerifyXMLSignature checks an enveloped XML signature on the whole
// document (one Signature directly under the root, Reference URI="")
// against the given certificates. It returns the certificate that signed.
func VerifyXMLSignature(b []byte, certs []*x509.Certificate) (*x509.Certificate, error) {
	doc, err := parseTree(b)
	if err != nil {
		return nil, err
	}
	root := doc.root()
	if root == nil {
		return nil, ErrNotOffline
	}
	var sig *node
	for _, k := range root.kids {
		if !k.isText && k.procInst == nil && k.local() == "Signature" {
			if sig != nil {
				return nil, ErrBadSignature
			}
			sig = k
		}
	}
	if sig == nil || sig.nsScope()[prefixOf(sig.name)] != dsigNS {
		return nil, ErrBadSignature
	}
	si := sig.child("SignedInfo")
	if si == nil {
		return nil, ErrBadSignature
	}
	if cm := si.child("CanonicalizationMethod"); cm == nil || cm.attr("Algorithm") != c14nAlg {
		return nil, fmt.Errorf("%w (unsupported canonicalisation)", ErrBadSignature)
	}
	var sigHash crypto.Hash
	switch sm := si.child("SignatureMethod"); {
	case sm == nil:
		return nil, ErrBadSignature
	case sm.attr("Algorithm") == dsigNS+"rsa-sha1":
		sigHash = crypto.SHA1
	case sm.attr("Algorithm") == "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256":
		sigHash = crypto.SHA256
	default:
		return nil, fmt.Errorf("%w (unsupported signature method)", ErrBadSignature)
	}
	var refs []*node
	for _, k := range si.kids {
		if !k.isText && k.local() == "Reference" {
			refs = append(refs, k)
		}
	}
	if len(refs) != 1 || refs[0].attr("URI") != "" {
		return nil, fmt.Errorf("%w (the signature must cover the whole file)", ErrBadSignature)
	}
	ref := refs[0]
	if ts := ref.child("Transforms"); ts != nil {
		for _, t := range ts.kids {
			if t.isText || t.procInst != nil {
				continue
			}
			if a := t.attr("Algorithm"); a != envelopAlg && a != c14nAlg {
				return nil, fmt.Errorf("%w (unsupported transform)", ErrBadSignature)
			}
		}
	}
	var dh func() hash.Hash
	switch dm := ref.child("DigestMethod"); {
	case dm == nil:
		return nil, ErrBadSignature
	case dm.attr("Algorithm") == dsigNS+"sha1":
		dh = sha1.New
	case dm.attr("Algorithm") == "http://www.w3.org/2001/04/xmlenc#sha256":
		dh = sha256.New
	default:
		return nil, fmt.Errorf("%w (unsupported digest)", ErrBadSignature)
	}
	dv := ref.child("DigestValue")
	if dv == nil {
		return nil, ErrBadSignature
	}
	want, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(dv.textContent()), ""))
	if err != nil {
		return nil, ErrBadSignature
	}
	h := dh()
	h.Write(c14n(doc, sig))
	if !hmac.Equal(h.Sum(nil), want) {
		return nil, ErrBadSignature
	}
	sv := sig.child("SignatureValue")
	if sv == nil {
		return nil, ErrBadSignature
	}
	sigBytes, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(sv.textContent()), ""))
	if err != nil {
		return nil, ErrBadSignature
	}
	hs := sigHash.New()
	hs.Write(c14n(si, nil))
	sum := hs.Sum(nil)
	for _, c := range certs {
		if pk, ok := c.PublicKey.(*rsa.PublicKey); ok && rsa.VerifyPKCS1v15(pk, sigHash, sum, sigBytes) == nil {
			return c, nil
		}
	}
	return nil, ErrBadSignature
}

func prefixOf(q string) string {
	if i := strings.IndexByte(q, ':'); i >= 0 {
		return q[:i]
	}
	return ""
}

// OfflineKYC is a verified offline e-KYC file.
type OfflineKYC struct {
	Aadhaar
	ReferenceID string
	mobileHash  string
	emailHash   string
	lastDigit   int
}

// ParseOfflineXML reads an offline e-KYC XML (OfflinePaperlessKyc). It
// does not check the signature: use VerifyOffline.
func ParseOfflineXML(b []byte) (OfflineKYC, error) {
	var o OfflineKYC
	doc, err := parseTree(b)
	if err != nil {
		return o, err
	}
	root := doc.root()
	if root == nil || root.local() != "OfflinePaperlessKyc" {
		return o, ErrNotOffline
	}
	o.ReferenceID = strings.TrimSpace(root.attr("referenceId"))
	if len(o.ReferenceID) < 4+14 || strings.Trim(o.ReferenceID, "0123456789") != "" {
		return o, ErrNotOffline
	}
	o.Last4, o.lastDigit = o.ReferenceID[:4], int(o.ReferenceID[3]-'0')
	t, err := time.ParseInLocation("20060102150405", o.ReferenceID[4:18], ist)
	if err != nil {
		return o, ErrNotOffline
	}
	o.Generated = t.Format(time.RFC3339)
	ud := root.child("UidData")
	if ud == nil {
		return o, ErrNotOffline
	}
	if poi := ud.child("Poi"); poi != nil {
		o.Name, o.Gender = strings.TrimSpace(poi.attr("name")), genderName(poi.attr("gender"))
		o.DOB = isoDate(poi.attr("dob"))
		if o.DOB == "" {
			o.DOB = strings.TrimSpace(poi.attr("dob")) // year of birth only
		}
		o.mobileHash, o.emailHash = strings.ToLower(strings.TrimSpace(poi.attr("m"))), strings.ToLower(strings.TrimSpace(poi.attr("e")))
	}
	if o.Name == "" {
		return o, ErrNotOffline
	}
	if poa := ud.child("Poa"); poa != nil {
		o.CareOf = strings.TrimSpace(poa.attr("careof"))
		var parts []string
		for _, k := range []string{"house", "street", "landmark", "loc", "vtc", "subdist", "po"} {
			if v := strings.TrimSpace(poa.attr(k)); v != "" && !contains(parts, v) {
				parts = append(parts, v)
			}
		}
		o.Address = strings.Join(parts, ", ")
		o.District, o.State, o.PIN = strings.TrimSpace(poa.attr("dist")), strings.TrimSpace(poa.attr("state")), strings.TrimSpace(poa.attr("pc"))
	}
	if ph := ud.child("Pht"); ph != nil {
		if img, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(ph.textContent()), "")); err == nil && len(img) > 0 {
			o.Photo, o.PhotoType = img, "image/jp2"
			if bytes.HasPrefix(img, []byte{0xFF, 0xD8}) {
				o.PhotoType = "image/jpeg"
			}
		}
	}
	h := sha256.Sum256(b)
	o.XML, o.SHA256, o.Source = b, hex.EncodeToString(h[:]), "Aadhaar offline e-KYC"
	return o, nil
}

var ist = time.FixedZone("IST", 5*3600+1800)

// offlineHash is UIDAI's hash of a mobile or email: SHA-256 of the value
// followed by the share code, repeated as many times as the last digit of
// the Aadhaar number (once for 0 or 1), each round over the previous
// round's hex.
func offlineHash(value, shareCode string, lastDigit int) string {
	n := lastDigit
	if n < 1 {
		n = 1
	}
	s := value + shareCode
	for i := 0; i < n; i++ {
		h := sha256.Sum256([]byte(s))
		s = hex.EncodeToString(h[:])
	}
	return s
}

// mobile10 is the last ten digits of an Indian mobile number.
func mobile10(m string) string {
	var d []byte
	for i := 0; i < len(m); i++ {
		if m[i] >= '0' && m[i] <= '9' {
			d = append(d, m[i])
		}
	}
	if len(d) > 10 {
		d = d[len(d)-10:]
	}
	return string(d)
}

func (o *OfflineKYC) match(mobile, email, shareCode string) {
	if o.mobileHash != "" && mobile != "" {
		o.MobileMatch = "Does not match"
		if hmac.Equal([]byte(offlineHash(mobile10(mobile), shareCode, o.lastDigit)), []byte(o.mobileHash)) {
			o.MobileMatch = "Matches"
		}
	}
	if o.emailHash != "" && email != "" {
		o.EmailMatch = "Does not match"
		for _, e := range []string{strings.TrimSpace(email), strings.ToLower(strings.TrimSpace(email))} {
			if hmac.Equal([]byte(offlineHash(e, shareCode, o.lastDigit)), []byte(o.emailHash)) {
				o.EmailMatch = "Matches"
			}
		}
	}
}

// VerifyOffline opens, checks and reads an offline e-KYC ZIP: share code,
// UIDAI signature, age (not older than OfflineMaxAge), and whether the
// mobile and email the investor confirmed are linked to the Aadhaar.
func VerifyOffline(zipData []byte, shareCode string, certs []*x509.Certificate, now time.Time, mobile, email string) (OfflineKYC, error) {
	shareCode = strings.TrimSpace(shareCode)
	if len(shareCode) != 4 || strings.Trim(shareCode, "0123456789") != "" {
		return OfflineKYC{}, errors.New("the share code is the 4 digits you chose when downloading the file")
	}
	x, err := OpenOfflineZip(zipData, shareCode)
	if err != nil {
		return OfflineKYC{}, err
	}
	if len(certs) == 0 {
		return OfflineKYC{}, errors.New("UIDAI's certificate is not set up on this service, so the file cannot be checked")
	}
	if _, err := VerifyXMLSignature(x, certs); err != nil {
		return OfflineKYC{}, err
	}
	o, err := ParseOfflineXML(x)
	if err != nil {
		return o, err
	}
	t, _ := time.Parse(time.RFC3339, o.Generated)
	if now.Sub(t) > OfflineMaxAge {
		return o, fmt.Errorf("this file was downloaded from UIDAI on %s; SEBI accepts files up to 3 days old, so download a new one", t.Format("2 Jan 2006"))
	}
	if t.Sub(now) > 10*time.Minute {
		return o, ErrNotOffline
	}
	o.match(mobile, email, shareCode)
	return o, nil
}

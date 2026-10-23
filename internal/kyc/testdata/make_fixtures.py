# Builds offline e-KYC fixtures with independent tools: lxml's C14N,
# cryptography's RSA, the zip command (ZipCrypto) and a WinZip-AES writer.
import base64, datetime, hashlib, hmac, os, struct, subprocess, sys, zlib, copy
from lxml import etree
from cryptography import x509
from cryptography.x509.oid import NameOID
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import rsa, padding
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
from cryptography.hazmat.primitives.kdf.pbkdf2 import PBKDF2HMAC

out = sys.argv[1]
key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "Test UIDAI offline signing")])
now = datetime.datetime(2026, 1, 1)
cert = (x509.CertificateBuilder().subject_name(name).issuer_name(name).public_key(key.public_key())
        .serial_number(1).not_valid_before(now).not_valid_after(now + datetime.timedelta(days=3650))
        .sign(key, hashes.SHA256()))
open(f"{out}/uidai-test.pem", "wb").write(cert.public_bytes(serialization.Encoding.PEM))
open(f"{out}/uidai-test.der", "wb").write(cert.public_bytes(serialization.Encoding.DER))
other = rsa.generate_private_key(public_exponent=65537, key_size=2048)

def h(value, code, last):
    s = value + code
    for _ in range(max(last, 1)):
        s = hashlib.sha256(s.encode()).hexdigest()
    return s

DS = "http://www.w3.org/2000/09/xmldsig#"
def make(ref, code, mobile, email, sig="rsa-sha1", signer=key, tamper=False):
    last = int(ref[3])
    photo = base64.b64encode(b"\x00\x00\x00\x0cjP  \r\n\x87\n" + os.urandom(200)).decode()
    xml = f'''<?xml version="1.0" encoding="UTF-8"?>
<OfflinePaperlessKyc referenceId="{ref}">
  <UidData>
    <Poi name="Kavya Iyer" m="{h(mobile, code, last)}" gender="F" e="{h(email, code, last)}" dob="02-03-1994"/>
    <Poa vtc="Bengaluru" street="12th Main &amp; 3rd Cross" subdist="Bengaluru North" state="Karnataka" pc="560038" po="Indiranagar" loc="HAL 2nd Stage" landmark="" house="742" dist="Bengaluru Urban" country="India" careof="D/O Ramesh Iyer"/>
    <Pht>{photo}</Pht>
  </UidData>
</OfflinePaperlessKyc>'''
    root = etree.fromstring(xml.encode())
    digest_alg = "http://www.w3.org/2001/04/xmlenc#sha256"
    dv = base64.b64encode(hashlib.sha256(etree.tostring(root, method="c14n")).digest()).decode()
    sm = {"rsa-sha1": DS + "rsa-sha1", "rsa-sha256": "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"}[sig]
    sigxml = f'''<Signature xmlns="{DS}"><SignedInfo><CanonicalizationMethod Algorithm="http://www.w3.org/TR/2001/REC-xml-c14n-20010315"/><SignatureMethod Algorithm="{sm}"/><Reference URI=""><Transforms><Transform Algorithm="{DS}enveloped-signature"/></Transforms><DigestMethod Algorithm="{digest_alg}"/><DigestValue>{dv}</DigestValue></Reference></SignedInfo><SignatureValue></SignatureValue><KeyInfo><X509Data><X509Certificate>{base64.b64encode(cert.public_bytes(serialization.Encoding.DER)).decode()}</X509Certificate></X509Data></KeyInfo></Signature>'''
    s = etree.fromstring(sigxml.encode())
    root.append(s)
    si = s.find(f"{{{DS}}}SignedInfo")
    c = etree.tostring(copy.deepcopy(si), method="c14n")  # a detached copy carries the inherited xmlns; in-place c14n of a sub-element adds stray xmlns=""
    hh = hashes.SHA1() if sig == "rsa-sha1" else hashes.SHA256()
    s.find(f"{{{DS}}}SignatureValue").text = base64.b64encode(signer.sign(c, padding.PKCS1v15(), hh)).decode()
    data = b'<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n' + etree.tostring(root)
    if tamper:
        data = data.replace(b'pc="560038"', b'pc="560039"')
    return data

def zipcrypto(path, inner, data, code):
    d = os.path.dirname(path); open(f"{d}/{inner}", "wb").write(data)
    if os.path.exists(path): os.remove(path)
    subprocess.check_call(["zip", "-q", "-j", "-P", code, path, f"{d}/{inner}"]); os.remove(f"{d}/{inner}")

def aeszip(path, inner, data, code, strength=3):
    kl = {1: 16, 2: 24, 3: 32}[strength]; salt = os.urandom(kl // 2)
    km = PBKDF2HMAC(algorithm=hashes.SHA1(), length=2 * kl + 2, salt=salt, iterations=1000).derive(code.encode())
    comp = zlib.compressobj(9, zlib.DEFLATED, -15); body = comp.compress(data) + comp.flush()
    enc = Cipher(algorithms.AES(km[:kl]), modes.ECB()).encryptor(); ct = bytearray()
    for i in range(0, len(body), 16):
        ctr = (i // 16 + 1).to_bytes(16, "little"); ks = enc.update(ctr)
        ct += bytes(a ^ b for a, b in zip(body[i:i + 16], ks))
    tag = hmac.new(km[kl:2 * kl], bytes(ct), hashlib.sha1).digest()[:10]
    payload = salt + km[2 * kl:] + bytes(ct) + tag
    extra = struct.pack("<HHHH", 0x9901, 7, 2, 0x4541) + struct.pack("<BH", strength, 8)
    n = inner.encode(); flags = 1
    lh = struct.pack("<IHHHHHIIIHH", 0x04034b50, 51, flags, 99, 0, 0x21, 0, len(payload), len(data), len(n), len(extra)) + n + extra
    cd = struct.pack("<IHHHHHHIIIHHHHHII", 0x02014b50, 51, 51, flags, 99, 0, 0x21, 0, len(payload), len(data), len(n), len(extra), 0, 0, 0, 0, 0) + n + extra
    end = struct.pack("<IHHHHIIH", 0x06054b50, 0, 0, 1, 1, len(cd), len(lh) + len(payload), 0)
    open(path, "wb").write(lh + payload + cd + end)

ref = "4322" + "20260115103015123"   # last digit 2, downloaded 15 Jan 2026 10:30:15 IST
zipcrypto(f"{out}/okyc-zipcrypto.zip", f"offlineaadhaar{ref}.xml", make(ref, "1234", "9876512345", "kavya@mail.example"), "1234")
aeszip(f"{out}/okyc-aes.zip", f"offlineaadhaar{ref}.xml", make(ref, "5678", "9876512345", "kavya@mail.example", sig="rsa-sha256"), "5678")
ref0 = "4320" + "20260115103015123"  # last digit 0: one round
zipcrypto(f"{out}/okyc-digit0.zip", "a.xml", make(ref0, "1111", "9876512345", "Kavya@Mail.example"), "1111")
zipcrypto(f"{out}/okyc-tampered.zip", "a.xml", make(ref, "1234", "9876512345", "x@y.z", tamper=True), "1234")
zipcrypto(f"{out}/okyc-wrongkey.zip", "a.xml", make(ref, "1234", "9876512345", "x@y.z", signer=other), "1234")
print("ok")

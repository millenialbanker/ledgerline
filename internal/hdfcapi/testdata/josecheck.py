# Independent check of internal/hdfcapi's JOSE (HDFC "JOSE High"): run
#   HDFCJOSE_DUMP=/tmp/jv go test -run NestedJWT ./internal/hdfcapi
#   python3 internal/hdfcapi/testdata/josecheck.py /tmp/jv/vectors.json
#   HDFCJOSE_IN=/tmp/jv/vectors.json go test -run Independent ./internal/hdfcapi
# Needs python's cryptography and PyJWT. Test keys only.
import json, base64, os, sys, jwt
from cryptography.hazmat.primitives.asymmetric import padding
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from cryptography.hazmat.primitives.serialization import load_der_private_key
def ub(s): return base64.urlsafe_b64decode(s + '=' * (-len(s) % 4))
def eb(b): return base64.urlsafe_b64encode(b).rstrip(b'=').decode()
v = json.load(open(sys.argv[1]))
def pk(b64):
    from cryptography.hazmat.primitives.asymmetric import rsa
    der = base64.b64decode(b64)
    # PKCS#1 RSAPrivateKey: load via PEM wrapper
    pem = b"-----BEGIN RSA PRIVATE KEY-----\n" + base64.encodebytes(der) + b"-----END RSA PRIVATE KEY-----\n"
    return serialization.load_pem_private_key(pem, None)
firm, bank = pk(v['firm']), pk(v['bank'])
oaep = padding.OAEP(mgf=padding.MGF1(hashes.SHA256()), algorithm=hashes.SHA256(), label=None)
# 1. Go -> Python: open the JWE with the bank key, verify the JWS with PyJWT.
h, ek, iv, ct, tag = v['jwe'].split('.')
hdr = json.loads(ub(h)); assert hdr == {"alg":"RSA-OAEP-256","enc":"A256GCM","typ":"JWE","kid":v['bkid']}, hdr
cek = bank.decrypt(ub(ek), oaep)
jws = AESGCM(cek).decrypt(ub(iv), ub(ct) + ub(tag), h.encode()).decode()
fpub = firm.public_key()
payload = jwt.api_jws.decode_complete(jws, key=fpub, algorithms=["RS256"])
assert payload['payload'].decode() == v['msg'] and payload['header']['kid'] == v['fkid'], payload
# kid the way HDFC's Java does it: SHA-256 of X509EncodedKeySpec (SPKI DER)
import hashlib
spki = fpub.public_bytes(serialization.Encoding.DER, serialization.PublicFormat.SubjectPublicKeyInfo)
assert eb(hashlib.sha256(spki).digest()) == v['fkid']
print("python opened and verified Go's message")
# 2. Python -> Go: a bank answer signed by the bank, encrypted to the firm.
ans = json.dumps({"batchnum": "PYTHON1", "txtstatus": "ACCEPTED"}).encode()
bspki = bank.public_key().public_bytes(serialization.Encoding.DER, serialization.PublicFormat.SubjectPublicKeyInfo)
bkid = eb(hashlib.sha256(bspki).digest()); assert bkid == v['bkid']
sjws = jwt.api_jws.encode(ans, bank, algorithm="RS256", headers={"kid": bkid, "typ": "JWS"})
hh = eb(json.dumps({"alg":"RSA-OAEP-256","enc":"A256GCM","kid":v['fkid']}).encode())
cek = os.urandom(32); iv = os.urandom(12)
sealed = AESGCM(cek).encrypt(iv, sjws.encode(), hh.encode())
out = '.'.join([hh, eb(firm.public_key().encrypt(cek, oaep)), eb(iv), eb(sealed[:-16]), eb(sealed[-16:])])
v['py_jwe'] = out; v['py_msg'] = ans.decode()
json.dump(v, open(sys.argv[1], 'w'))
print("python wrote a message for Go")

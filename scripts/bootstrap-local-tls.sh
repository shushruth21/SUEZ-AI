#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
if [ -f "$ROOT_DIR/.env" ]; then
  # shellcheck disable=SC1091
  . "$ROOT_DIR/.env"
fi

EDGE_HOST=${EDGE_HOST:-suez.acrosystemsinc.ai}
CERT_DIR="$ROOT_DIR/infra/tls/generated"
CERT_FILE="$CERT_DIR/localhost.crt"
KEY_FILE="$CERT_DIR/localhost.key"
CA_CERT_FILE="$CERT_DIR/rootCA.pem"
CA_KEY_FILE="$CERT_DIR/rootCA-key.pem"
CSR_FILE="$CERT_DIR/localhost.csr"
EXT_FILE="$CERT_DIR/localhost.ext"
FULLCHAIN_FILE="$CERT_DIR/localhost-fullchain.pem"

mkdir -p "$CERT_DIR"

if [ ! -f "$CA_CERT_FILE" ] || [ ! -f "$CA_KEY_FILE" ]; then
  openssl req -x509 -nodes -newkey rsa:4096 \
    -keyout "$CA_KEY_FILE" \
    -out "$CA_CERT_FILE" \
    -days 3650 \
    -subj "/CN=SUEZ Local Root CA" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -addext "subjectKeyIdentifier=hash"
fi

cat >"$EXT_FILE" <<'EOF'
basicConstraints=CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid,issuer
EOF

printf 'subjectAltName=DNS:%s,IP:127.0.0.1\n' "$EDGE_HOST" >>"$EXT_FILE"

openssl req -new -nodes -newkey rsa:2048 \
  -keyout "$KEY_FILE" \
  -out "$CSR_FILE" \
  -subj "/CN=$EDGE_HOST"

openssl x509 -req \
  -in "$CSR_FILE" \
  -CA "$CA_CERT_FILE" \
  -CAkey "$CA_KEY_FILE" \
  -CAcreateserial \
  -out "$CERT_FILE" \
  -days 825 \
  -sha256 \
  -extfile "$EXT_FILE"

rm -f "$CSR_FILE" "$EXT_FILE"

cat "$CERT_FILE" "$CA_CERT_FILE" >"$FULLCHAIN_FILE"

FINGERPRINT=$(
  openssl x509 -in "$CA_CERT_FILE" -noout -fingerprint -sha1 |
    sed 's/.*=//' |
    tr -d ':'
)

if security find-certificate -a -Z ~/Library/Keychains/login.keychain-db | tr -d ' :' | grep -q "$FINGERPRINT"; then
  printf 'Trusted SUEZ local root CA already present in login keychain.\n'
else
  security add-trusted-cert -d -r trustRoot -k ~/Library/Keychains/login.keychain-db "$CA_CERT_FILE"
  printf 'Trusted SUEZ local root CA in login keychain.\n'
fi

printf 'TLS assets ready for %s at %s, %s, and %s\n' "$EDGE_HOST" "$CERT_FILE" "$KEY_FILE" "$FULLCHAIN_FILE"

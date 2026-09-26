#!/bin/sh
# Issue a persistent local CA and a short-lived server certificate for one LAN IPv4 address.
set -eu

usage() {
    echo 'usage: cert.sh <LAN IPv4 address> <certificate directory> [--renew-leaf]' >&2
    exit 2
}

[ "$#" -eq 2 ] || [ "$#" -eq 3 ] || usage
lan_ip=$1
cert_dir=$2
renew_leaf=${3:-}
[ -z "$renew_leaf" ] || [ "$renew_leaf" = --renew-leaf ] || usage

python3 - "$lan_ip" <<'PY' || usage
import ipaddress
import sys

try:
    address = ipaddress.IPv4Address(sys.argv[1])
except ipaddress.AddressValueError:
    sys.exit(1)
if str(address) != sys.argv[1] or address.is_unspecified or address.is_multicast:
    sys.exit(1)
PY

command -v openssl >/dev/null || { echo 'cert.sh: OpenSSL is required' >&2; exit 1; }
command -v flock >/dev/null || { echo 'cert.sh: flock is required' >&2; exit 1; }
umask 077
[ ! -L "$cert_dir" ] || { echo 'cert.sh: certificate directory must not be a symlink' >&2; exit 1; }
mkdir -p "$cert_dir"
cert_dir=$(cd "$cert_dir" && pwd -P)
[ "$(stat -c %u "$cert_dir")" = "$(id -u)" ] || {
    echo 'cert.sh: certificate directory must be owned by the current user' >&2
    exit 1
}
chmod 0700 "$cert_dir"

# Keep two concurrent starts from issuing different keys for the same leaf.
exec 9>"$cert_dir/.issue.lock"
flock -x 9

for name in ca.key ca.crt leaf.key leaf.crt; do
    [ ! -L "$cert_dir/$name" ] || { echo "cert.sh: $name must not be a symlink" >&2; exit 1; }
done

temp_dir=$(mktemp -d "$cert_dir/.issue.XXXXXXXX")
trap 'rm -rf "$temp_dir"' EXIT HUP INT TERM

if [ -e "$cert_dir/ca.key" ] || [ -e "$cert_dir/ca.crt" ]; then
    [ -f "$cert_dir/ca.key" ] && [ -f "$cert_dir/ca.crt" ] || {
        echo 'cert.sh: incomplete CA material; refusing to replace the trust anchor' >&2
        exit 1
    }
    [ "$(stat -c %u "$cert_dir/ca.key")" = "$(id -u)" ] || {
        echo 'cert.sh: CA key must be owned by the current user' >&2
        exit 1
    }
    chmod 0600 "$cert_dir/ca.key"
    openssl x509 -in "$cert_dir/ca.crt" -pubkey -noout >"$temp_dir/ca-cert.pub"
    openssl pkey -in "$cert_dir/ca.key" -pubout >"$temp_dir/ca-key.pub"
    cmp -s "$temp_dir/ca-cert.pub" "$temp_dir/ca-key.pub" || {
        echo 'cert.sh: CA certificate and key do not match' >&2
        exit 1
    }
    openssl verify -CAfile "$cert_dir/ca.crt" "$cert_dir/ca.crt" >/dev/null || {
        echo 'cert.sh: CA certificate validation failed' >&2
        exit 1
    }
    openssl x509 -in "$cert_dir/ca.crt" -noout -checkend 7776000 >/dev/null || {
        echo 'cert.sh: CA expires within 90 days; rotate it with an explicit trust migration' >&2
        exit 1
    }
else
    openssl ecparam -name prime256v1 -genkey -noout -out "$temp_dir/ca.key"
    openssl req -new -x509 -sha256 -days 1825 \
        -key "$temp_dir/ca.key" -out "$temp_dir/ca.crt" \
        -subj '/CN=WorkOS LAN local CA' \
        -addext 'basicConstraints=critical,CA:TRUE,pathlen:0' \
        -addext 'keyUsage=critical,keyCertSign,cRLSign'
    install -m 0600 "$temp_dir/ca.key" "$cert_dir/ca.key"
    install -m 0644 "$temp_dir/ca.crt" "$cert_dir/ca.crt"
    echo 'cert.sh: created local CA; each client must trust ca.crt once' >&2
fi

valid_leaf=false
if [ -z "$renew_leaf" ] && [ -f "$cert_dir/leaf.key" ] && [ -f "$cert_dir/leaf.crt" ]; then
    if openssl verify -purpose sslserver -CAfile "$cert_dir/ca.crt" "$cert_dir/leaf.crt" >/dev/null 2>&1 &&
        openssl x509 -in "$cert_dir/leaf.crt" -noout -checkend 1209600 >/dev/null 2>&1 &&
        openssl x509 -in "$cert_dir/leaf.crt" -noout -ext subjectAltName |
            tr ',' '\n' | sed 's/^[[:space:]]*//' | grep -Fxq "IP Address:$lan_ip"; then
        openssl x509 -in "$cert_dir/leaf.crt" -pubkey -noout >"$temp_dir/leaf-cert.pub"
        openssl pkey -in "$cert_dir/leaf.key" -pubout >"$temp_dir/leaf-key.pub"
        if cmp -s "$temp_dir/leaf-cert.pub" "$temp_dir/leaf-key.pub"; then
            valid_leaf=true
        fi
    fi
fi

if [ "$valid_leaf" = false ]; then
    openssl ecparam -name prime256v1 -genkey -noout -out "$temp_dir/leaf.key"
    openssl req -new -sha256 -key "$temp_dir/leaf.key" -out "$temp_dir/leaf.csr" \
        -subj '/CN=WorkOS LAN Gateway'
    cat >"$temp_dir/leaf.ext" <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth
subjectAltName=IP:$lan_ip
EOF
    serial=$(openssl rand -hex 16)
    openssl x509 -req -sha256 -days 90 -in "$temp_dir/leaf.csr" \
        -CA "$cert_dir/ca.crt" -CAkey "$cert_dir/ca.key" -set_serial "0x$serial" \
        -extfile "$temp_dir/leaf.ext" -out "$temp_dir/leaf.crt" 2>/dev/null
    openssl verify -purpose sslserver -CAfile "$cert_dir/ca.crt" "$temp_dir/leaf.crt" >/dev/null
    install -m 0600 "$temp_dir/leaf.key" "$cert_dir/leaf.key"
    install -m 0644 "$temp_dir/leaf.crt" "$cert_dir/leaf.crt"
    echo "cert.sh: issued server certificate for $lan_ip" >&2
else
    echo "cert.sh: reusing server certificate for $lan_ip" >&2
fi

# Never broaden the key permissions when reusing material from an older run.
chmod 0600 "$cert_dir/leaf.key"
printf 'CA certificate: %s/ca.crt\n' "$cert_dir"
printf 'CA SHA-256 fingerprint: '
openssl x509 -in "$cert_dir/ca.crt" -noout -fingerprint -sha256 | sed 's/^[^=]*=//'

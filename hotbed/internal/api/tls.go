package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"hotbed/internal/env"
)

// SnakeoilTLS loads (or generates once) the dashboard's self-signed cert at a
// stable path under the state dir (~/.local/state/hotbed/tls), so the
// fingerprint stays pinned across restarts — restarting the daemon must never
// re-trust-prompt the browser. The honest contract: the cert IS self-signed
// ("snakeoil"), browsers warn once, the printed sha256 fingerprint is what the
// user compares against the browser warning, and deleting the file pair
// regenerates.
//
// Why at all: navigator.clipboard needs a SECURE CONTEXT — plain http on a
// LAN box kills the dashboard's copy buttons — and a LAN dashboard over
// plaintext http is its own liability. HOTBEDD_TLS (serve) flips the dashboard
// listener to TLS using this cert.
func SnakeoilTLS(bindHost string) (tls.Certificate, string, string, error) {
	dir := filepath.Join(env.StateDir(), "tls")
	certPath := filepath.Join(dir, "hotbedd-cert.pem")
	keyPath := filepath.Join(dir, "hotbedd-key.pem")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, "", "", fmt.Errorf("state dir: %w", err)
	}
	if crt, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return crt, certPath, fingerprintPEM(certPath), nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", "", fmt.Errorf("keygen: %w", err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	tpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{Organization: []string{"hotbed"}, CommonName: "hotbedd-snakeoil"},
		NotBefore:    time.Now().Add(-24 * time.Hour),
		NotAfter:     time.Now().AddDate(2, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     snakeoilNames(),
		IPAddresses:  snakeoilIPs(bindHost),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tpl, &tpl, key.Public(), key)
	if err != nil {
		return tls.Certificate{}, "", "", fmt.Errorf("certgen: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", "", fmt.Errorf("keymarshal: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, "", "", fmt.Errorf("write cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, "", "", fmt.Errorf("write key: %w", err)
	}
	sum := sha256.Sum256(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		certPath, hex.EncodeToString(sum[:]), nil
}

// snakeoilNames: localhost + this hostname — IPs carry the SAN load.
func snakeoilNames() []string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return []string{"localhost"}
	}
	return []string{"localhost", host}
}

// snakeoilIPs: loopback, the bound address (if an IP), and every unicast
// interface address — a self-signed cert can't be doubted more, but the SANs
// make the browser warning state the exact claim it's making.
func snakeoilIPs(bindHost string) []net.IP {
	seen := map[string]bool{}
	var out []net.IP
	add := func(s string) {
		ip, err := netip.ParseAddr(s)
		if err != nil || seen[ip.String()] {
			return
		}
		seen[ip.String()] = true
		out = append(out, net.ParseIP(s)) // 4-byte for v4, 16-byte for v6
	}
	add("127.0.0.1")
	add("::1")
	add(bindHost)
	if ifaces, err := net.Interfaces(); err == nil {
		for _, ifc := range ifaces {
			addrs, aerr := ifc.Addrs()
			if aerr != nil {
				continue
			}
			for _, addr := range addrs {
				add(trimZone(addr.String()))
			}
		}
	}
	return out
}

func trimZone(s string) string {
	// strip %zone (fe80::1%eth0) — netip.ParseAddr chokes on the zone
	for j := 0; j < len(s); j++ {
		if s[j] == '%' {
			return s[:j]
		}
	}
	return s
}

// fingerprintPEM pins on the EXISTING file (restart path) — same DER, same
// sha256 as a first-run generation would print.
func fingerprintPEM(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var der []byte
	for {
		var block *pem.Block
		block, raw = pem.Decode(raw)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			der = append(der, block.Bytes...)
			break
		}
	}
	if der == nil {
		return ""
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

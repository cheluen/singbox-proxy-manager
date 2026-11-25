package services

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func testInboundTLSPEM(t *testing.T, template *x509.Certificate) ([]byte, []byte, []byte) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "测试签发者"},
		NotBefore: time.Now().Add(-24 * time.Hour), NotAfter: time.Now().Add(48 * time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	issuerDER, err := x509.CreateCertificate(rand.Reader, issuer, issuer, &issuerKey.PublicKey, issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	if template.SerialNumber == nil {
		template.SerialNumber = big.NewInt(2)
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, issuer, &privateKey.PublicKey, issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	chainPEM := append(append([]byte{}, certificatePEM...), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuerDER})...)
	return certificatePEM, keyPEM, chainPEM
}

func TestInboundTLSLegacyPrivateKeyFormats(t *testing.T) {
	for _, format := range []string{"ec", "rsa"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			setInboundTLSTestDirectories(t, root, filepath.Join(root, "missing"))
			var key crypto.Signer
			var keyDER []byte
			var blockType string
			if format == "rsa" {
				privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
				if err != nil {
					t.Fatal(err)
				}
				key, keyDER, blockType = privateKey, x509.MarshalPKCS1PrivateKey(privateKey), "RSA PRIVATE KEY"
			} else {
				privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				keyDER, err = x509.MarshalECPrivateKey(privateKey)
				if err != nil {
					t.Fatal(err)
				}
				key, blockType = privateKey, "EC PRIVATE KEY"
			}
			template := testInboundTLSTemplate(format + ".example")
			template.SerialNumber = big.NewInt(1)
			certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
			if err != nil {
				t.Fatal(err)
			}
			writeInboundTLSFile(t, filepath.Join(root, "cert.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}))
			writeInboundTLSFile(t, filepath.Join(root, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: keyDER}))
			if status := ResolveInboundTLS("", ""); !status.Ready || len(status.Certificates) != 1 {
				t.Fatalf("未识别传统 %s 私钥格式：%+v", format, status)
			}
		})
	}
}

func TestInboundTLSAutoSelectionCountsOnlyCurrentCertificates(t *testing.T) {
	root := t.TempDir()
	setInboundTLSTestDirectories(t, root, filepath.Join(root, "missing"))
	validCertificate, validKey, _ := testInboundTLSPEM(t, testInboundTLSTemplate("valid.example"))
	expired := testInboundTLSTemplate("expired.example")
	expired.NotBefore, expired.NotAfter = time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour)
	expiredCertificate, expiredKey, _ := testInboundTLSPEM(t, expired)
	writeInboundTLSFile(t, filepath.Join(root, "valid.pem"), append(validCertificate, validKey...))
	writeInboundTLSFile(t, filepath.Join(root, "expired.pem"), append(expiredCertificate, expiredKey...))
	status := ResolveInboundTLS(filepath.Join(root, "expired.pem"), filepath.Join(root, "expired.pem"))
	if !status.Ready || status.SelectionMode != "auto" || len(status.Certificates) != 2 || status.Active.Domains[0] != "valid.example" {
		t.Fatalf("过期证书影响了唯一有效证书的自动选择：%+v", status)
	}
}

func testInboundTLSTemplate(name string) *x509.Certificate {
	return &x509.Certificate{
		Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
}

func writeInboundTLSFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func setInboundTLSTestDirectories(t *testing.T, certDirectory, keyDirectory string) {
	t.Helper()
	t.Setenv("SBPM_INBOUND_TLS_CERT_DIR", certDirectory)
	t.Setenv("SBPM_INBOUND_TLS_KEY_DIR", keyDirectory)
	t.Setenv("INBOUND_TLS_CERT_DIR", "")
	t.Setenv("INBOUND_TLS_KEY_DIR", "")
}

func TestInboundTLSMissingAndEmptyDirectories(t *testing.T) {
	root := t.TempDir()
	setInboundTLSTestDirectories(t, filepath.Join(root, "missing"), root)
	status := ResolveInboundTLS("", "")
	if status.Ready || status.SelectionMode != "none" || status.Active != nil || status.Certificates == nil || len(status.Certificates) != 0 {
		t.Fatalf("空证书目录状态错误：%+v", status)
	}
	if _, err := status.RequireCertificate(); err == nil {
		t.Fatal("无证书时没有给出错误")
	}
}

func TestInboundTLSCrossDirectoryPairingAndSelection(t *testing.T) {
	root := t.TempDir()
	certDirectory, keyDirectory := filepath.Join(root, "certs"), filepath.Join(root, "keys")
	setInboundTLSTestDirectories(t, certDirectory, keyDirectory)
	template := testInboundTLSTemplate("z.example")
	template.DNSNames = append(template.DNSNames, "alias.example")
	template.IPAddresses = []net.IP{net.ParseIP("192.0.2.7"), net.ParseIP("2001:db8::1")}
	certificatePEM, keyPEM, _ := testInboundTLSPEM(t, template)
	certPath, keyPath := filepath.Join(certDirectory, "nested", "certificate.data"), filepath.Join(keyDirectory, "key.data")
	writeInboundTLSFile(t, certPath, certificatePEM)
	writeInboundTLSFile(t, keyPath, keyPEM)
	status := ResolveInboundTLS("stale-cert", "stale-key")
	if !status.Ready || status.SelectionMode != "auto" || len(status.Certificates) != 1 || status.Active.CertPath != certPath || status.Active.KeyPath != keyPath || status.Active.Expired || !slices.Equal(status.Active.Domains, []string{"z.example", "alias.example", "192.0.2.7", "2001:db8::1"}) {
		t.Fatalf("跨目录自动配对失败：%+v", status)
	}
	status = ResolveInboundTLS(certPath, keyPath)
	if !status.Ready || status.SelectionMode != "selected" || status.SelectedCertPath != certPath || status.SelectedKeyPath != keyPath {
		t.Fatalf("保存的证书未选中：%+v", status)
	}
	otherCertificate, otherKey, _ := testInboundTLSPEM(t, testInboundTLSTemplate("a.example"))
	writeInboundTLSFile(t, filepath.Join(certDirectory, "other.pem"), otherCertificate)
	writeInboundTLSFile(t, filepath.Join(keyDirectory, "other.key"), otherKey)
	status = ResolveInboundTLS("", "")
	if status.Ready || status.SelectionMode != "unselected" || len(status.Certificates) != 2 || status.Certificates[0].Domains[0] != "a.example" {
		t.Fatalf("多证书未选择状态错误：%+v", status)
	}
	if _, err := status.RequireCertificate(); err == nil || !strings.Contains(err.Error(), "选择") {
		t.Fatalf("多证书提示不明确：%v", err)
	}
	status = ResolveInboundTLS(certPath, keyPath)
	if !status.Ready || status.SelectionMode != "selected" || status.Active.CertPath != certPath {
		t.Fatalf("多证书中保存的配对失效：%+v", status)
	}
	status = ResolveInboundTLS(certPath, "stale-key")
	if status.Ready || status.SelectionMode != "unselected" {
		t.Fatalf("失效选择未要求重新选择：%+v", status)
	}
}

func TestInboundTLSSymlinksDeduplicateAndPreferFullChain(t *testing.T) {
	root := t.TempDir()
	certDirectory, keyDirectory := filepath.Join(root, "certs"), filepath.Join(root, "keys")
	setInboundTLSTestDirectories(t, certDirectory, keyDirectory)
	certificatePEM, keyPEM, chainPEM := testInboundTLSPEM(t, testInboundTLSTemplate("renew.example"))
	archive, live := filepath.Join(certDirectory, "archive", "renew.example"), filepath.Join(certDirectory, "live", "renew.example")
	writeInboundTLSFile(t, filepath.Join(archive, "cert1.pem"), certificatePEM)
	writeInboundTLSFile(t, filepath.Join(archive, "fullchain1.pem"), chainPEM)
	writeInboundTLSFile(t, filepath.Join(archive, "privkey1.pem"), keyPEM)
	if err := os.MkdirAll(live, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, names := range [][2]string{{"cert.pem", "cert1.pem"}, {"fullchain.pem", "fullchain1.pem"}, {"privkey.pem", "privkey1.pem"}} {
		if err := os.Symlink(filepath.Join("..", "..", "archive", "renew.example", names[1]), filepath.Join(live, names[0])); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(certDirectory, keyDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(certDirectory, filepath.Join(archive, "cycle")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(live, "broken.pem")); err != nil {
		t.Fatal(err)
	}
	status := ResolveInboundTLS("", "")
	wantCert, wantKey := filepath.Join(live, "fullchain.pem"), filepath.Join(keyDirectory, "live", "renew.example", "privkey.pem")
	if !status.Ready || len(status.Certificates) != 1 || status.Active.CertPath != wantCert || status.Active.KeyPath != wantKey {
		t.Fatalf("软链接、重复挂载或 fullchain 优先级错误：%+v", status)
	}
	newCertificate, newKey, newChain := testInboundTLSPEM(t, testInboundTLSTemplate("renew.example"))
	writeInboundTLSFile(t, filepath.Join(archive, "cert2.pem"), newCertificate)
	writeInboundTLSFile(t, filepath.Join(archive, "fullchain2.pem"), newChain)
	writeInboundTLSFile(t, filepath.Join(archive, "privkey2.pem"), newKey)
	for _, names := range [][2]string{{"cert.pem", "cert2.pem"}, {"fullchain.pem", "fullchain2.pem"}, {"privkey.pem", "privkey2.pem"}} {
		path := filepath.Join(live, names[0])
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..", "..", "archive", "renew.example", names[1]), path); err != nil {
			t.Fatal(err)
		}
	}
	status = ResolveInboundTLS(wantCert, wantKey)
	if !status.Ready || status.SelectionMode != "selected" || status.Active.CertPath != wantCert || status.Active.KeyPath != wantKey {
		t.Fatalf("续签后逻辑路径未保持有效：%+v", status)
	}
}

func TestInboundTLSValidityLimitsAndCombinedPEM(t *testing.T) {
	root := t.TempDir()
	setInboundTLSTestDirectories(t, root, filepath.Join(root, "missing"))
	template := testInboundTLSTemplate("common-name.example")
	template.DNSNames = nil
	certificatePEM, keyPEM, _ := testInboundTLSPEM(t, template)
	combined := append(append([]byte{}, certificatePEM...), keyPEM...)
	writeInboundTLSFile(t, filepath.Join(root, "combined.pem"), combined)
	oversizedCertificate, oversizedKey, _ := testInboundTLSPEM(t, testInboundTLSTemplate("oversized.example"))
	writeInboundTLSFile(t, filepath.Join(root, "oversized.pem"), append(append(oversizedCertificate, oversizedKey...), make([]byte, inboundTLSMaxFileSize)...))
	deepPath := filepath.Join(root, strings.Repeat("deep/", inboundTLSMaxDepth+1), "combined.pem")
	deepCertificate, deepKey, _ := testInboundTLSPEM(t, testInboundTLSTemplate("deep.example"))
	writeInboundTLSFile(t, deepPath, append(deepCertificate, deepKey...))
	writeInboundTLSFile(t, filepath.Join(root, "invalid.pem"), []byte("-----BEGIN CERTIFICATE-----\naW52YWxpZA==\n-----END CERTIFICATE-----\n"))
	status := ResolveInboundTLS("", "")
	if !status.Ready || len(status.Certificates) != 1 || status.Active.CertPath != status.Active.KeyPath || !slices.Equal(status.Active.Domains, []string{"common-name.example"}) {
		t.Fatalf("混合 PEM、域名回退或扫描限制失败：%+v", status)
	}
	for _, test := range []struct {
		name   string
		future bool
	}{{"expired", false}, {"not-yet-valid", true}} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			setInboundTLSTestDirectories(t, directory, filepath.Join(directory, "missing"))
			invalid := testInboundTLSTemplate("invalid-time.example")
			if test.future {
				invalid.NotBefore = time.Now().Add(time.Hour)
			} else {
				invalid.NotBefore = time.Now().Add(-48 * time.Hour)
				invalid.NotAfter = time.Now().Add(-time.Hour)
			}
			certificate, key, _ := testInboundTLSPEM(t, invalid)
			writeInboundTLSFile(t, filepath.Join(directory, "combined.pem"), append(certificate, key...))
			status := ResolveInboundTLS("", "")
			if status.Ready || status.SelectionMode != "none" || len(status.Certificates) != 1 || status.Certificates[0].Expired != !test.future {
				t.Fatalf("证书有效期校验失败：%+v", status)
			}
		})
	}
}

func TestInboundTLSDirectoryOverridesAndMismatchedKey(t *testing.T) {
	root := t.TempDir()
	certDirectory, keyDirectory := filepath.Join(root, "certs"), filepath.Join(root, "keys")
	t.Setenv("SBPM_INBOUND_TLS_CERT_DIR", "")
	t.Setenv("SBPM_INBOUND_TLS_KEY_DIR", "")
	t.Setenv("INBOUND_TLS_CERT_DIR", certDirectory)
	t.Setenv("INBOUND_TLS_KEY_DIR", keyDirectory)
	certificate, key, _ := testInboundTLSPEM(t, testInboundTLSTemplate("match.example"))
	_, mismatchedKey, _ := testInboundTLSPEM(t, testInboundTLSTemplate("other.example"))
	writeInboundTLSFile(t, filepath.Join(certDirectory, "cert.pem"), certificate)
	writeInboundTLSFile(t, filepath.Join(keyDirectory, "wrong.key"), mismatchedKey)
	if status := ResolveInboundTLS("", ""); status.Ready || len(status.Certificates) != 0 {
		t.Fatalf("接受了公钥不匹配的证书和私钥：%+v", status)
	}
	writeInboundTLSFile(t, filepath.Join(keyDirectory, "right.key"), key)
	if status := ResolveInboundTLS("", ""); !status.Ready || status.Active.CertPath != filepath.Join(certDirectory, "cert.pem") {
		t.Fatalf("通用目录环境变量未生效：%+v", status)
	}
	t.Setenv("SBPM_INBOUND_TLS_CERT_DIR", filepath.Join(root, "container-certs"))
	t.Setenv("SBPM_INBOUND_TLS_KEY_DIR", filepath.Join(root, "container-keys"))
	if status := ResolveInboundTLS("", ""); status.Ready || len(status.Certificates) != 0 {
		t.Fatalf("容器目录变量未覆盖宿主机路径：%+v", status)
	}
}

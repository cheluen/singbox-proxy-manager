package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	_ "embed"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"
)

// 公开证书来源: https://crt.sh/?d=18519019583，不包含私钥。
//
//go:embed testdata/compromised-intermediate.pem
var compromisedIntermediatePEM []byte

func TestCVE2026111943RejectsCompromisedCertificate(t *testing.T) {
	block, _ := pem.Decode(compromisedIntermediatePEM)
	if block == nil {
		t.Fatal("无法读取安全回归证书")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	if hex.EncodeToString(hash[:]) != "ebed6475c2c2d80fcb199f3fe8dd6e6b3ce60b77eb15d4e42c3dec0c6f5b5105" {
		t.Fatal("回归证书的公钥与 Go 官方拒绝列表不一致")
	}

	// 即使旧信任库显式信任该证书，标准库也必须拒绝它。
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	chains, err := cert.Verify(x509.VerifyOptions{
		Roots:       roots,
		CurrentTime: cert.NotBefore.Add(time.Hour),
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	var invalid x509.CertificateInvalidError
	if len(chains) != 0 || !errors.As(err, &invalid) || invalid.Reason != x509.NoValidChains {
		t.Fatalf("已失信证书未被安全补丁拒绝: chains=%d err=%v", len(chains), err)
	}
}

func TestCertificateVerificationStillAcceptsValidTrust(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "安全回归测试 CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	chains, err := cert.Verify(x509.VerifyOptions{
		Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	if err != nil || len(chains) != 1 {
		t.Fatalf("正常信任链受到影响: chains=%d err=%v", len(chains), err)
	}
}

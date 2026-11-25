package services

import (
	"crypto"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	inboundTLSMaxDepth    = 10
	inboundTLSMaxFileSize = 2 << 20
)

type InboundTLSCertificate struct {
	CertPath   string    `json:"cert_path"`
	KeyPath    string    `json:"key_path"`
	Domains    []string  `json:"domains"`
	CommonName string    `json:"common_name"`
	NotBefore  time.Time `json:"not_before"`
	NotAfter   time.Time `json:"not_after"`
	Expired    bool      `json:"expired"`
}

type InboundTLSStatus struct {
	Ready            bool                    `json:"ready"`
	SelectionMode    string                  `json:"selection_mode"`
	SelectedCertPath string                  `json:"selected_cert_path"`
	SelectedKeyPath  string                  `json:"selected_key_path"`
	Active           *InboundTLSCertificate  `json:"active,omitempty"`
	Certificates     []InboundTLSCertificate `json:"certificates"`
}

func (status InboundTLSStatus) RequireCertificate() (*InboundTLSCertificate, error) {
	if status.Ready {
		return status.Active, nil
	}
	if status.SelectionMode == "unselected" {
		return nil, errors.New("发现多套可用的入站 TLS 证书，请先在设置中选择证书")
	}
	return nil, errors.New("未发现可用的入站 TLS 证书，请配置有效的证书与私钥")
}

func ResolveInboundTLS(savedCertPath, savedKeyPath string) InboundTLSStatus {
	now := time.Now()
	status := InboundTLSStatus{
		SelectionMode: "none", SelectedCertPath: savedCertPath, SelectedKeyPath: savedKeyPath,
		Certificates: scanInboundTLSCertificates(now),
	}
	var valid []int
	for index, certificate := range status.Certificates {
		if certificate.Expired || now.Before(certificate.NotBefore) {
			continue
		}
		valid = append(valid, index)
		if certificate.CertPath == savedCertPath && certificate.KeyPath == savedKeyPath {
			status.Ready, status.SelectionMode, status.Active = true, "selected", &status.Certificates[index]
			return status
		}
	}
	if len(valid) == 1 {
		status.Ready, status.SelectionMode, status.Active = true, "auto", &status.Certificates[valid[0]]
	} else if len(valid) > 1 {
		status.SelectionMode = "unselected"
	}
	return status
}

func inboundTLSDirectory(primary, fallback, defaultPath string) string {
	for _, name := range []string{primary, fallback} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return defaultPath
}

type inboundTLSFile struct {
	path        string
	links       int
	certRoot    bool
	leaf        *x509.Certificate
	chainLength int
	keyID       *[sha256.Size]byte
}

func walkInboundTLSFiles(root string, visit func(string, int)) {
	ancestors := map[string]bool{}
	var walk func(string, int, int)
	walk = func(path string, depth, links int) {
		if depth > inboundTLSMaxDepth {
			return
		}
		info, err := os.Lstat(path)
		if err != nil {
			return
		}
		if info.Mode()&os.ModeSymlink != 0 {
			links++
			info, err = os.Stat(path)
			if err != nil {
				return
			}
		}
		if !info.IsDir() {
			if info.Mode().IsRegular() && info.Size() > 0 && info.Size() <= inboundTLSMaxFileSize {
				visit(path, links)
			}
			return
		}
		realPath, err := filepath.EvalSymlinks(path)
		if err != nil || ancestors[realPath] {
			return
		}
		ancestors[realPath] = true
		defer delete(ancestors, realPath)
		entries, err := os.ReadDir(path)
		if err != nil {
			return
		}
		for _, entry := range entries {
			walk(filepath.Join(path, entry.Name()), depth+1, links)
		}
	}
	if absolute, err := filepath.Abs(root); err == nil {
		walk(absolute, 0, 0)
	}
}

func inboundTLSPublicKeyID(publicKey any) ([sha256.Size]byte, error) {
	encoded, err := x509.MarshalPKIXPublicKey(publicKey)
	return sha256.Sum256(encoded), err
}

func parseInboundTLSFile(path string, links int, certRoot bool) inboundTLSFile {
	candidate := inboundTLSFile{path: path, links: links, certRoot: certRoot}
	file, err := os.Open(path)
	if err != nil {
		return candidate
	}
	data, err := io.ReadAll(io.LimitReader(file, inboundTLSMaxFileSize+1))
	_ = file.Close()
	if err != nil || len(data) > inboundTLSMaxFileSize {
		return candidate
	}
	invalidChain := false
	for {
		block, rest := pem.Decode(data)
		if block == nil {
			break
		}
		data = rest
		if block.Type == "CERTIFICATE" {
			certificate, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				invalidChain = true
				continue
			}
			if candidate.leaf == nil {
				candidate.leaf = certificate
			}
			candidate.chainLength++
		} else if strings.HasSuffix(block.Type, "PRIVATE KEY") {
			var privateKey any
			switch block.Type {
			case "PRIVATE KEY":
				privateKey, err = x509.ParsePKCS8PrivateKey(block.Bytes)
			case "RSA PRIVATE KEY":
				privateKey, err = x509.ParsePKCS1PrivateKey(block.Bytes)
			case "EC PRIVATE KEY":
				privateKey, err = x509.ParseECPrivateKey(block.Bytes)
			}
			if signer, ok := privateKey.(crypto.Signer); err == nil && ok {
				if identity, err := inboundTLSPublicKeyID(signer.Public()); err == nil {
					candidate.keyID = &identity
				}
			}
		}
	}
	if invalidChain {
		candidate.leaf = nil
	}
	return candidate
}

func preferInboundTLSFile(candidate, previous inboundTLSFile, certificate bool) bool {
	if certificate && candidate.chainLength != previous.chainLength {
		return candidate.chainLength > previous.chainLength
	}
	if candidate.certRoot != previous.certRoot {
		return candidate.certRoot == certificate
	}
	if candidate.links != previous.links {
		return candidate.links > previous.links
	}
	return candidate.path < previous.path
}

func scanInboundTLSCertificates(now time.Time) []InboundTLSCertificate {
	certDirectory := inboundTLSDirectory("SBPM_INBOUND_TLS_CERT_DIR", "INBOUND_TLS_CERT_DIR", "/app/certs")
	keyDirectory := inboundTLSDirectory("SBPM_INBOUND_TLS_KEY_DIR", "INBOUND_TLS_KEY_DIR", "/app/keys")
	var certificates []inboundTLSFile
	keys := make(map[[sha256.Size]byte][]inboundTLSFile)
	for index, directory := range []string{certDirectory, keyDirectory} {
		walkInboundTLSFiles(directory, func(path string, links int) {
			candidate := parseInboundTLSFile(path, links, index == 0)
			if candidate.leaf != nil {
				certificates = append(certificates, candidate)
			}
			if candidate.keyID != nil {
				keys[*candidate.keyID] = append(keys[*candidate.keyID], candidate)
			}
		})
	}
	sort.Slice(certificates, func(first, second int) bool {
		return preferInboundTLSFile(certificates[first], certificates[second], true)
	})
	for _, candidates := range keys {
		sort.Slice(candidates, func(first, second int) bool {
			return preferInboundTLSFile(candidates[first], candidates[second], false)
		})
	}
	type identity struct{ certificate, publicKey [sha256.Size]byte }
	seen := map[identity]bool{}
	result := make([]InboundTLSCertificate, 0)
	for _, candidate := range certificates {
		publicKey, err := inboundTLSPublicKeyID(candidate.leaf.PublicKey)
		if err != nil {
			continue
		}
		fingerprint := identity{certificate: sha256.Sum256(candidate.leaf.Raw), publicKey: publicKey}
		if seen[fingerprint] {
			continue
		}
		for _, key := range keys[publicKey] {
			pair, err := tls.LoadX509KeyPair(candidate.path, key.path)
			if err != nil {
				continue
			}
			leaf, err := x509.ParseCertificate(pair.Certificate[0])
			if err != nil {
				continue
			}
			loadedPublicKey, err := inboundTLSPublicKeyID(leaf.PublicKey)
			if err != nil {
				continue
			}
			fingerprint = identity{certificate: sha256.Sum256(leaf.Raw), publicKey: loadedPublicKey}
			if seen[fingerprint] {
				break
			}
			domains := append([]string{}, leaf.DNSNames...)
			for _, address := range leaf.IPAddresses {
				domains = append(domains, address.String())
			}
			if len(domains) == 0 && leaf.Subject.CommonName != "" {
				domains = append(domains, leaf.Subject.CommonName)
			}
			result = append(result, InboundTLSCertificate{
				CertPath: candidate.path, KeyPath: key.path, Domains: domains, CommonName: leaf.Subject.CommonName,
				NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter, Expired: !now.Before(leaf.NotAfter),
			})
			seen[fingerprint] = true
			break
		}
	}
	primaryDomain := func(certificate InboundTLSCertificate) string {
		if len(certificate.Domains) > 0 {
			return certificate.Domains[0]
		}
		return certificate.CommonName
	}
	sort.Slice(result, func(first, second int) bool {
		left, right := result[first], result[second]
		if leftDomain, rightDomain := primaryDomain(left), primaryDomain(right); leftDomain != rightDomain {
			return leftDomain < rightDomain
		}
		if left.CertPath != right.CertPath {
			return left.CertPath < right.CertPath
		}
		return left.KeyPath < right.KeyPath
	})
	return result
}

package trusttunnel

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
)

var (
	acmeChallenges = make(map[string]string)
	acmeMu         sync.RWMutex
)

// RegisterChallenge stores a challenge token and its key authorization for HTTP-01.
func RegisterChallenge(token, keyAuth string) {
	acmeMu.Lock()
	defer acmeMu.Unlock()
	acmeChallenges[token] = keyAuth
}

// GetChallenge retrieves a key authorization for a given token.
func GetChallenge(token string) (string, bool) {
	acmeMu.RLock()
	defer acmeMu.RUnlock()
	val, ok := acmeChallenges[token]
	return val, ok
}

// DeregisterChallenge deletes a challenge token.
func DeregisterChallenge(token string) {
	acmeMu.Lock()
	defer acmeMu.Unlock()
	delete(acmeChallenges, token)
}

// getOrCreateAccountKey loads or generates the ACME account private key.
func getOrCreateAccountKey(dataDir string) (*ecdsa.PrivateKey, error) {
	keyPath := filepath.Join(dataDir, "certs", "acme_account_key.pem")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0755); err != nil {
		return nil, err
	}

	if data, err := os.ReadFile(keyPath); err == nil {
		block, _ := pem.Decode(data)
		if block != nil && block.Type == "EC PRIVATE KEY" {
			key, err := x509.ParseECPrivateKey(block.Bytes)
			if err == nil {
				return key, nil
			}
		}
	}

	// Generate new ECDSA key
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate account ecdsa key: %w", err)
	}

	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}

	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyBytes,
	})

	if err := os.WriteFile(keyPath, pemData, 0600); err != nil {
		return nil, fmt.Errorf("failed to write account key: %w", err)
	}

	return key, nil
}

// GenerateCertificate requests a Let's Encrypt certificate for the given hostname.
func GenerateCertificate(ctx context.Context, hostname, email, dataDir string) (string, string, error) {
	// 1. Get or create account key
	accountKey, err := getOrCreateAccountKey(dataDir)
	if err != nil {
		return "", "", fmt.Errorf("failed to get ACME account key: %w", err)
	}

	// 2. Initialize ACME client
	dirURL := acme.LetsEncryptURL
	if os.Getenv("LETSENCRYPT_STAGING") == "true" {
		dirURL = "https://acme-staging-v02.api.letsencrypt.org/directory"
	}

	client := &acme.Client{
		Key:          accountKey,
		DirectoryURL: dirURL,
	}

	// Register account
	var contacts []string
	if email != "" {
		contacts = []string{"mailto:" + email}
	}
	_, err = client.Register(ctx, &acme.Account{Contact: contacts}, acme.AcceptTOS)
	if err != nil {
		// Ignore if already registered
		if err.Error() != "acme: account already exists" {
			// Ignore conflict status as well
			if ae, ok := err.(*acme.Error); !ok || ae.StatusCode != http.StatusConflict {
				return "", "", fmt.Errorf("failed to register ACME account: %w", err)
			}
		}
	}

	// 3. Create Order
	identifiers := []acme.AuthzID{{Type: "dns", Value: hostname}}
	order, err := client.AuthorizeOrder(ctx, identifiers)
	if err != nil {
		return "", "", fmt.Errorf("failed to authorize order: %w", err)
	}

	// 4. Handle HTTP-01 Challenges
	for _, authURL := range order.AuthzURLs {
		authz, err := client.GetAuthorization(ctx, authURL)
		if err != nil {
			return "", "", fmt.Errorf("failed to get authorization %s: %w", authURL, err)
		}

		if authz.Status == acme.StatusValid {
			continue
		}

		// Find http-01 challenge
		var challenge *acme.Challenge
		for _, chal := range authz.Challenges {
			if chal.Type == "http-01" {
				challenge = chal
				break
			}
		}

		if challenge == nil {
			return "", "", fmt.Errorf("http-01 challenge not found for authorization %s", authURL)
		}

		// Generate challenge key authorization
		keyAuth, err := client.HTTP01ChallengeResponse(challenge.Token)
		if err != nil {
			return "", "", fmt.Errorf("failed to generate HTTP-01 challenge response: %w", err)
		}

		// Register the challenge token globally so our Gin handler can serve it
		RegisterChallenge(challenge.Token, keyAuth)
		defer DeregisterChallenge(challenge.Token)

		// Accept challenge to notify Let's Encrypt
		_, err = client.Accept(ctx, challenge)
		if err != nil {
			return "", "", fmt.Errorf("failed to accept challenge: %w", err)
		}

		// Wait for authorization to succeed
		_, err = client.WaitAuthorization(ctx, authURL)
		if err != nil {
			return "", "", fmt.Errorf("ACME authorization failed: %w", err)
		}
	}

	// 5. Wait for order to be complete/ready
	_, err = client.WaitOrder(ctx, order.URI)
	if err != nil {
		return "", "", fmt.Errorf("waiting for order failed: %w", err)
	}

	// 6. Generate Certificate Private Key (ECDSA P-256 for optimal security/performance)
	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate cert private key: %w", err)
	}

	// 7. Create CSR (Certificate Signing Request)
	template := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: hostname},
		DNSNames: []string{hostname},
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, template, certKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to create certificate signing request: %w", err)
	}

	// 8. Finalize Order and download cert chain (bundle = true)
	derChain, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return "", "", fmt.Errorf("failed to finalize certificate order: %w", err)
	}

	// 9. PEM Encode Certificate Chain
	var certPEM []byte
	for _, der := range derChain {
		certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: der,
		})...)
	}

	// 10. PEM Encode Certificate Private Key
	certKeyBytes, err := x509.MarshalECPrivateKey(certKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal cert private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: certKeyBytes,
	})

	// 11. Write cert & key to persistent directory
	certDir := filepath.Join(dataDir, "certs", hostname)
	if err := os.MkdirAll(certDir, 0755); err != nil {
		return "", "", fmt.Errorf("failed to create cert storage directory: %w", err)
	}

	certPath := filepath.Join(certDir, "cert.pem")
	keyPath := filepath.Join(certDir, "key.pem")

	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		return "", "", fmt.Errorf("failed to write certificate file: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return "", "", fmt.Errorf("failed to write private key file: %w", err)
	}

	absCertPath, _ := filepath.Abs(certPath)
	absKeyPath, _ := filepath.Abs(keyPath)

	return absCertPath, absKeyPath, nil
}

// GenerateSelfSignedCert creates a self-signed TLS certificate and private key.
func GenerateSelfSignedCert(hostname, dataDir string) (string, string, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}

	notBefore := time.Now()
	notAfter := notBefore.Add(365 * 24 * time.Hour) // 1 year validity

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return "", "", err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"CleverConnect VPN"},
			CommonName:   hostname,
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{hostname},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return "", "", err
	}

	certDir := filepath.Join(dataDir, "certs", hostname)
	if err := os.MkdirAll(certDir, 0755); err != nil {
		return "", "", err
	}

	certPath := filepath.Join(certDir, "self_signed_cert.pem")
	keyPath := filepath.Join(certDir, "self_signed_key.pem")

	certOut, err := os.Create(certPath)
	if err != nil {
		return "", "", err
	}
	defer certOut.Close()
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return "", "", err
	}
	defer keyOut.Close()
	privBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return "", "", err
	}
	pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes})

	absCert, _ := filepath.Abs(certPath)
	absKey, _ := filepath.Abs(keyPath)
	return absCert, absKey, nil
}


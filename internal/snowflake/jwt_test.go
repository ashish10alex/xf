package snowflake

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeyPairJWT(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	keyFile := filepath.Join(t.TempDir(), "rsa_key.p8")
	if err := os.WriteFile(keyFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	fn := keyPairToken("acme-prod.us-east-1.aws", "svc_xf", "", keyFile, "")
	tok, err := fn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.Type != TokenKeyPairJWT {
		t.Fatalf("type = %q, want %q", tok.Type, TokenKeyPairJWT)
	}
	parts := strings.Split(tok.Value, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt parts = %d", len(parts))
	}
	// Verify RS256 signature with the public key.
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&priv.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("signature invalid: %v", err)
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Iss string `json:"iss"`
		Sub string `json:"sub"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Sub != "ACME-PROD.SVC_XF" {
		t.Fatalf("sub = %q, want ACME-PROD.SVC_XF", claims.Sub)
	}
	if !strings.HasPrefix(claims.Iss, "ACME-PROD.SVC_XF.SHA256:") {
		t.Fatalf("iss = %q", claims.Iss)
	}
	if claims.Exp <= claims.Iat {
		t.Fatalf("exp %d <= iat %d", claims.Exp, claims.Iat)
	}
}

func TestQualifiedAccountAndEncryptedKeyHint(t *testing.T) {
	if got := qualifiedAccount("acme_corp-prod"); got != "ACME_CORP-PROD" {
		t.Fatalf("qualifiedAccount underscore = %q, want ACME_CORP-PROD", got)
	}
	if got := AccountHost("acme_corp-prod"); got != "https://acme-corp-prod.snowflakecomputing.com" {
		t.Fatalf("AccountHost underscore = %q", got)
	}
	if got := qualifiedAccount("xy12345-abc.global"); got != "XY12345" {
		t.Fatalf("qualifiedAccount .global = %q, want XY12345", got)
	}

	encPKCS8 := pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte("dummy")})
	if _, _, err := parseRSAPrivateKey(encPKCS8, "secret"); err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Fatalf("PKCS#8 encrypted err = %v, want encrypted hint", err)
	}
	encPKCS1 := pem.EncodeToMemory(&pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-128-CBC,00"},
		Bytes:   []byte("dummy"),
	})
	if _, _, err := parseRSAPrivateKey(encPKCS1, ""); err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Fatalf("PKCS#1 encrypted err = %v, want encrypted hint", err)
	}
}

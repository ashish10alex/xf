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
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// keyPairToken signs short-lived RS256 JWTs for Snowflake key-pair
// authentication (X-Snowflake-Authorization-Token-Type: KEYPAIR_JWT), caching
// each token for 50 minutes (Snowflake accepts up to 60 minutes).
func keyPairToken(account, user, pemText, keyPath, passphrase string) func(context.Context) (Token, error) {
	var mu sync.Mutex
	var cached string
	var until time.Time
	var priv *rsa.PrivateKey
	var fp string

	return func(ctx context.Context) (Token, error) {
		mu.Lock()
		defer mu.Unlock()
		if cached != "" && time.Now().Before(until) {
			return Token{Value: cached, Type: TokenKeyPairJWT}, nil
		}
		if priv == nil {
			if account == "" {
				return Token{}, authErr(errors.New("key-pair auth requires a Snowflake account"))
			}
			if user == "" {
				return Token{}, authErr(errors.New("key-pair auth requires a Snowflake user"))
			}
			raw := []byte(pemText)
			if len(raw) == 0 && keyPath != "" {
				var err error
				raw, err = os.ReadFile(expandTilde(keyPath))
				if err != nil {
					return Token{}, authErr(fmt.Errorf("read private key %s: %w", keyPath, err))
				}
			}
			var err error
			priv, fp, err = parseRSAPrivateKey(raw, passphrase)
			if err != nil {
				return Token{}, authErr(err)
			}
		}
		now := time.Now().UTC()
		tok, err := signSnowflakeJWT(priv, account, user, fp, now)
		if err != nil {
			return Token{}, authErr(err)
		}
		cached = tok
		until = now.Add(50 * time.Minute)
		return Token{Value: tok, Type: TokenKeyPairJWT}, nil
	}
}

// qualifiedAccount normalizes a Snowflake account identifier for the JWT
// issuer and subject claims: uppercase, with any region/cloud suffix stripped
// (e.g. "xy12345.us-east-1.aws" -> "XY12345", "acme-prod" -> "ACME-PROD").
func qualifiedAccount(account string) string {
	account = strings.TrimSpace(account)
	if i := strings.Index(strings.ToLower(account), ".snowflakecomputing.com"); i >= 0 {
		account = account[:i]
	}
	if i := strings.Index(account, "://"); i >= 0 {
		account = account[i+3:]
	}
	// Strip region/cloud segment (e.g. .us-east-1.aws) unless it is a global
	// account locator without region; Snowflake's JWT spec strips everything
	// from the first '.' onward unless ".global" is used (where everything
	// from '-' onward is stripped).
	if strings.Contains(strings.ToLower(account), ".global") {
		if i := strings.IndexByte(account, '-'); i >= 0 {
			account = account[:i]
		}
	} else if i := strings.IndexByte(account, '.'); i >= 0 {
		account = account[:i]
	}
	return strings.ToUpper(account)
}

// parseRSAPrivateKey parses an unencrypted PKCS#8 or PKCS#1 PEM (or base64
// DER/PEM) RSA private key and computes its Snowflake SHA256 public key
// fingerprint ("SHA256:<base64>").
func parseRSAPrivateKey(raw []byte, passphrase string) (*rsa.PrivateKey, string, error) {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 {
		return nil, "", errors.New("empty private key")
	}
	// Support dbt's base64-encoded private_key env var as well as raw PEM.
	if !strings.HasPrefix(string(raw), "-----BEGIN") {
		if dec, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(raw)), "")); err == nil {
			raw = dec
		}
	}
	var der []byte
	var encrypted bool
	if block, _ := pem.Decode(raw); block != nil {
		der = block.Bytes
		encrypted = strings.Contains(strings.ToUpper(block.Type), "ENCRYPTED") ||
			strings.Contains(strings.ToUpper(block.Headers["Proc-Type"]), "ENCRYPTED")
	} else {
		der = raw
	}
	if encrypted {
		hint := "encrypted private keys are not supported directly; use an unencrypted key or set SNOWFLAKE_TOKEN (a programmatic access token)"
		if passphrase == "" {
			hint = "private key is encrypted; decrypt it or set SNOWFLAKE_TOKEN (a programmatic access token)"
		}
		return nil, "", errors.New(hint)
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, "", fmt.Errorf("private key is %T, want RSA", k)
		}
		key = rk
	} else if rk, err2 := x509.ParsePKCS1PrivateKey(der); err2 == nil {
		key = rk
	} else {
		return nil, "", fmt.Errorf("parse RSA private key: %w", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, "", fmt.Errorf("marshal public key: %w", err)
	}
	sum := sha256.Sum256(pubDER)
	return key, "SHA256:" + base64.StdEncoding.EncodeToString(sum[:]), nil
}

func signSnowflakeJWT(key *rsa.PrivateKey, account, user, fingerprint string, now time.Time) (string, error) {
	qualUser := qualifiedAccount(account) + "." + strings.ToUpper(strings.TrimSpace(user))
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]any{
		"iss": qualUser + "." + fingerprint,
		"sub": qualUser,
		"iat": now.Unix(),
		"exp": now.Add(59 * time.Minute).Unix(),
	})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(header) + "." + enc.EncodeToString(payload)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + enc.EncodeToString(sig), nil
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

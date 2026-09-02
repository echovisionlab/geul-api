package pat

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestGeneratedTokenRoundTripsToOneWayVerifier(t *testing.T) {
	t.Parallel()

	randomBytes := sequentialBytes(tokenSelectorBytes + tokenSecretBytes)
	credential, err := generateCredential(bytes.NewReader(randomBytes))
	if err != nil {
		t.Fatalf("generateCredential() error = %v", err)
	}
	raw := credential.secret.Reveal()
	if !strings.HasPrefix(raw, tokenPrefix) {
		t.Fatalf("token = %q, want prefix %q", raw, tokenPrefix)
	}

	parsedID, candidate, err := parseToken(raw)
	if err != nil {
		t.Fatalf("parseToken() error = %v", err)
	}
	if parsedID != credential.id {
		t.Fatalf("parsed token ID = %q, want %q", parsedID, credential.id)
	}
	if !credential.verifier.matches(candidate) {
		t.Fatal("generated verifier did not match parsed token")
	}

	_, secretText, _ := strings.Cut(strings.TrimPrefix(raw, tokenPrefix), ".")
	secret, err := base64.RawURLEncoding.DecodeString(secretText)
	if err != nil {
		t.Fatalf("decode generated secret: %v", err)
	}
	wantDigest := sha256.Sum256(secret)
	if !bytes.Equal(credential.verifier.Bytes(), wantDigest[:]) {
		t.Fatal("stored verifier is not the SHA-256 one-way verifier")
	}
	if bytes.Equal(credential.verifier.Bytes(), secret) {
		t.Fatal("stored verifier contains the plaintext secret")
	}
}

func TestTokenParserRejectsEveryMalformedBoundary(t *testing.T) {
	t.Parallel()

	credential, err := generateCredential(bytes.NewReader(sequentialBytes(tokenSelectorBytes + tokenSecretBytes)))
	if err != nil {
		t.Fatalf("generateCredential() error = %v", err)
	}
	raw := credential.secret.Reveal()
	remainder := strings.TrimPrefix(raw, tokenPrefix)
	selector, secret, _ := strings.Cut(remainder, ".")

	malformed := []string{
		"",
		" " + raw,
		raw + " ",
		"Bearer " + raw,
		strings.ToUpper(tokenPrefix) + remainder,
		tokenPrefix + selector,
		tokenPrefix + "." + secret,
		tokenPrefix + selector + ".",
		tokenPrefix + selector + "." + secret + ".extra",
		tokenPrefix + selector[:len(selector)-1] + "." + secret,
		tokenPrefix + selector + "=.\u200b" + secret,
		tokenPrefix + selector + "." + secret[:len(secret)-1],
		tokenPrefix + selector + "." + secret + "=",
		tokenPrefix + strings.Repeat("*", len(selector)) + "." + secret,
	}
	for _, value := range malformed {
		if _, _, err := parseToken(value); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("parseToken(%q) error = %v, want ErrInvalidToken", value, err)
		}
	}
}

func TestTokenGenerationRejectsShortRandomSources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reader io.Reader
	}{
		{name: "selector", reader: bytes.NewReader(make([]byte, tokenSelectorBytes-1))},
		{name: "secret", reader: io.MultiReader(bytes.NewReader(make([]byte, tokenSelectorBytes)), errorReader{})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			credential, err := generateCredential(test.reader)
			if err == nil {
				t.Fatal("generateCredential() unexpectedly succeeded")
			}
			if credential.secret.Reveal() != "" || credential.id != "" || credential.verifier.valid() {
				t.Fatalf("failed generation returned credential material: %#v", credential)
			}
		})
	}
}

func TestSecretAndVerifierFormattingAlwaysRedacts(t *testing.T) {
	t.Parallel()

	credential, err := generateCredential(bytes.NewReader(sequentialBytes(tokenSelectorBytes + tokenSecretBytes)))
	if err != nil {
		t.Fatalf("generateCredential() error = %v", err)
	}
	plaintext := credential.secret.Reveal()
	for _, rendered := range []string{
		fmt.Sprint(credential.secret),
		fmt.Sprintf("%v", credential.secret),
		fmt.Sprintf("%+v", credential.secret),
		fmt.Sprintf("%#v", credential.secret),
		fmt.Sprint(credential.verifier),
		fmt.Sprintf("%#v", credential.verifier),
	} {
		if strings.Contains(rendered, plaintext) || !strings.Contains(rendered, "REDACTED") {
			t.Fatalf("credential formatting was not redacted: %q", rendered)
		}
	}
}

func TestVerifierBytesAreDefensiveAndLengthChecked(t *testing.T) {
	t.Parallel()

	digest := sha256.Sum256([]byte("secret"))
	verifier, err := VerifierFromBytes(digest[:])
	if err != nil {
		t.Fatalf("VerifierFromBytes() error = %v", err)
	}
	digest[0] ^= 0xff
	first := verifier.Bytes()
	first[0] ^= 0xff
	if bytes.Equal(first, verifier.Bytes()) {
		t.Fatal("Verifier.Bytes() did not return a defensive copy")
	}
	for _, invalid := range [][]byte{nil, make([]byte, sha256.Size-1), make([]byte, sha256.Size+1)} {
		if _, err := VerifierFromBytes(invalid); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("VerifierFromBytes(length=%d) error = %v", len(invalid), err)
		}
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("random source failed") }

func sequentialBytes(length int) []byte {
	value := make([]byte, length)
	for index := range value {
		value[index] = byte(index + 1)
	}
	return value
}

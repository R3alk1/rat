package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
)

func GenKeyPair() (*ecdh.PrivateKey, []byte, error) {
	curve := ecdh.X25519()
	priv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return priv, priv.PublicKey().Bytes(), nil
}

func GetAESKey(priv *ecdh.PrivateKey, peerPubBytes []byte) ([]byte, error) {
	curve := ecdh.X25519()
	peerPub, err := curve.NewPublicKey(peerPubBytes)
	if err != nil {
		return nil, err
	}
	secret, err := priv.ECDH(peerPub)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(secret)
	return hash[:], nil
}

func Encrypt(plaintext, key []byte) ([]byte, []byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, aesgcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	return aesgcm.Seal(nil, nonce, plaintext, nil), nonce, nil
}

func Decrypt(ciphertext, nonce, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != aesgcm.NonceSize() {
		return nil, errors.New("Invalid NonceSize")
	}
	return aesgcm.Open(nil, nonce, ciphertext, nil)
}

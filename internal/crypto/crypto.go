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

// генерация пары ключей ecdh на кривой x25519
func GenKeyPair() (*ecdh.PrivateKey, []byte, error) {
	curve := ecdh.X25519()
	priv, err := curve.GenerateKey(rand.Reader) // криптографически стойкая генерация
	if err != nil {
		return nil, nil, err
	}
	return priv, priv.PublicKey().Bytes(), nil
}

// вычисление общего aes-ключа
func GetAESKey(priv *ecdh.PrivateKey, peerPubBytes []byte) ([]byte, error) {
	curve := ecdh.X25519()
	peerPub, err := curve.NewPublicKey(peerPubBytes)
	if err != nil {
		return nil, err
	}
	secret, err := priv.ECDH(peerPub) // вычисление секрета
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(secret) // хешируем, чтобы получить равномерно распределенный ключ
	return hash[:], nil           // возврат 32 байт
}

// шифрование алгоритмом aes-256-gcm
func Encrypt(plaintext, key []byte) ([]byte, []byte, error) {
	block, err := aes.NewCipher(key) // создается aes шифр с ключом 32 байта
	if err != nil {
		return nil, nil, err
	}
	aesgcm, err := cipher.NewGCM(block) // оборачиваем в gcm для аутентификационного шифрования
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, aesgcm.NonceSize()) // 12 байт для gcm
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	return aesgcm.Seal(nil, nonce, plaintext, nil), nonce, nil // шифруем и добавляем тег аутентификации в конец шифротекста
}

// дешифрование
func Decrypt(ciphertext, nonce, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key) // создается шифр
	if err != nil {
		return nil, err
	}
	aesgcm, err := cipher.NewGCM(block) // оборачивается в gcm
	if err != nil {
		return nil, err
	}
	if len(nonce) != aesgcm.NonceSize() {
		return nil, errors.New("Invalid NonceSize") // защита от некорректного тега
	}
	return aesgcm.Open(nil, nonce, ciphertext, nil) // дешифрование и проверка тега аутентифиакции
}

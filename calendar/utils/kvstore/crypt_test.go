// Copyright (c) 2019-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package kvstore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestEncryptDecrypt(t *testing.T) {
	for _, test := range []struct {
		Name          string
		Key           []byte
		ExpectedError string
	}{
		{
			Name:          "EncryptDecrypt: Invalid key",
			Key:           make([]byte, 1),
			ExpectedError: "could not create a cipher block",
		},
		{
			Name: "EncryptDecrypt: Valid",
			Key:  make([]byte, 16),
		},
	} {
		t.Run(test.Name, func(t *testing.T) {
			assert := assert.New(t)
			encryptedKey, err := encrypt(test.Key, []byte("mockData"))
			if test.ExpectedError != "" {
				assert.Contains(err.Error(), test.ExpectedError)
				assert.Equal([]byte(""), encryptedKey)
			} else {
				assert.Nil(err)
				assert.NotEqual([]byte(""), encryptedKey)
			}

			decryptedKey, err := decrypt(test.Key, encryptedKey)
			if test.ExpectedError != "" {
				assert.Contains(err.Error(), test.ExpectedError)
				assert.Equal([]byte(""), decryptedKey)
			} else {
				assert.Nil(err)
				assert.Equal([]byte("mockData"), decryptedKey)
			}
		})
	}
}

func TestEncrypt(t *testing.T) {
	for _, test := range []struct {
		Name          string
		Key           []byte
		ExpectedError string
	}{
		{
			Name:          "Encrypt: Invalid key",
			Key:           make([]byte, 1),
			ExpectedError: "could not create a cipher block",
		},
		{
			Name: "Encrypt: Valid",
			Key:  make([]byte, 16),
		},
	} {
		t.Run(test.Name, func(t *testing.T) {
			assert := assert.New(t)
			resp, err := encrypt(test.Key, []byte("mockData"))
			if test.ExpectedError != "" {
				assert.Contains(err.Error(), test.ExpectedError)
				assert.Equal([]byte(""), resp)
			} else {
				assert.Nil(err)
				assert.NotEqual([]byte(""), resp)
			}
		})
	}
}

func TestDecrypt(t *testing.T) {
	for _, test := range []struct {
		Name          string
		Key           []byte
		Text          string
		ExpectedError string
	}{
		{
			Name:          "Decrypt: Invalid key",
			Text:          "8qhtxbdZSjFi4-YBVmJ8nWgW2iQEoLrt8sVRTsTxm3awzvG-",
			Key:           make([]byte, 1),
			ExpectedError: "could not create a cipher block",
		},
		{
			Name: "Decrypt: Valid",
			Text: "8qhtxbdZSjFi4-YBVmJ8nWgW2iQEoLrt8sVRTsTxm3awzvG-",
			Key:  make([]byte, 16),
		},
	} {
		t.Run(test.Name, func(t *testing.T) {
			assert := assert.New(t)
			resp, err := decrypt(test.Key, []byte(test.Text))
			if test.ExpectedError != "" {
				assert.Contains(err.Error(), test.ExpectedError)
				assert.Equal([]byte(""), resp)
			} else {
				assert.Nil(err)
				assert.Equal([]byte("mockData"), resp)
			}
		})
	}
}

func TestEncryptedKeyStoreWrongKey(t *testing.T) {
	oldKey := []byte("0123456789abcdef0123456789abcdef")
	newKey := []byte("fedcba9876543210fedcba9876543210")

	var stored []byte
	m := &mockKVStore{}
	m.On("Store", "user", mock.Anything).Run(func(args mock.Arguments) {
		stored = args.Get(1).([]byte)
	}).Return(nil)
	require.NoError(t, NewEncryptedKeyStore(m, oldKey).Store("user", []byte(`{"a":1}`)))
	m.On("Load", "user").Return(stored, nil)

	_, err := NewEncryptedKeyStore(m, newKey).Load("user")
	require.ErrorIs(t, err, ErrDecrypt, "data stored with the old key cannot be read with the new one")

	plain, err := NewEncryptedKeyStore(m, oldKey).Load("user")
	require.NoError(t, err)
	require.Equal(t, `{"a":1}`, string(plain))
}

func TestEncryptedKeyStoreInvalidKeyIsNotDecryptError(t *testing.T) {
	m := &mockKVStore{}
	m.On("Load", "user").Return([]byte("anything"), nil)

	_, err := NewEncryptedKeyStore(m, []byte("short")).Load("user")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrDecrypt, "an invalid key is a configuration error, not lost data")
}

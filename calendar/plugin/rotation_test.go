// Copyright (c) 2019-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-mscalendar/calendar/engine"
	"github.com/mattermost/mattermost-plugin-mscalendar/calendar/remote"
	"github.com/mattermost/mattermost-plugin-mscalendar/calendar/store"
	"github.com/mattermost/mattermost-plugin-mscalendar/calendar/store/mock_store"
	"github.com/mattermost/mattermost-plugin-mscalendar/calendar/utils/bot"
	"github.com/mattermost/mattermost-plugin-mscalendar/calendar/utils/kvstore"
)

const (
	rotationMMUserID = "mockMMUserID"
	// The hashed user key of rotationMMUserID.
	rotationUserKey = "user_c3b5020d58a049787bc969768465b890"
	rotationOldKey  = "0123456789abcdef0123456789abcdef"
)

// rotationBot records the users it sends a DM to. Other bot methods are not used by the rotation code.
type rotationBot struct {
	bot.Bot
	dms []string
}

func (b *rotationBot) DM(mattermostUserID, _ string, _ ...interface{}) (string, error) {
	b.dms = append(b.dms, mattermostUserID)
	return "post_id", nil
}

// captureKV keeps the last stored value, to build data encrypted with the old key.
type captureKV struct{ data []byte }

func (c *captureKV) Load(string) ([]byte, error)          { return c.data, nil }
func (c *captureKV) Store(_ string, data []byte) error    { c.data = data; return nil }
func (c *captureKV) StoreTTL(string, []byte, int64) error { return nil }
func (c *captureKV) StoreWithOptions(string, []byte, model.PluginKVSetOptions) (bool, error) {
	return true, nil
}
func (c *captureKV) Delete(string) error             { return nil }
func (c *captureKV) List(int, int) ([]string, error) { return nil, nil }

func encryptedWithOldKey(t *testing.T) []byte {
	data, err := json.Marshal(&store.User{MattermostUserID: rotationMMUserID, Remote: &remote.User{ID: "remoteID"}})
	require.NoError(t, err)
	c := &captureKV{}
	require.NoError(t, kvstore.NewEncryptedKeyStore(c, []byte(rotationOldKey)).Store("key", data))
	return c.data
}

func notEncryptedWithOldKey(*testing.T) []byte { return []byte("not encrypted with the old key") }

// warned reports whether LogWarn was called with the message.
func warned(api *plugintest.API, msg string) bool {
	for _, call := range api.Calls {
		if call.Method == "LogWarn" && len(call.Arguments) > 0 && call.Arguments.String(0) == msg {
			return true
		}
	}
	return false
}

func TestReEncryptUserData(t *testing.T) {
	unreadable := fmt.Errorf("%w: cipher: message authentication failed", kvstore.ErrDecrypt)
	kvErr := errors.New("failed plugin KVGet")

	tcs := []struct {
		name string
		// The stored user as read with the previous key, or the KVGet error.
		oldKeyData  func(t *testing.T) []byte
		oldKeyKVErr *model.AppError
		// The result of loading the user with the new key, when the previous key fails.
		loadsWithNewKey bool
		newKeyErr       error
		storeUserFails  bool
		forceDelete     bool
		forceDeleteErr  error
		expectedWarn    string
		expectDM        bool
	}{
		{
			// In a cluster, another node already re-encrypted the user, or a token refresh or a reconnect
			// stored it with the new key.
			name:            "user already encrypted with the new key is left alone",
			oldKeyData:      notEncryptedWithOldKey,
			loadsWithNewKey: true,
		},
		{
			name:         "user unreadable with both keys is force-deleted and notified",
			oldKeyData:   notEncryptedWithOldKey,
			newKeyErr:    unreadable,
			forceDelete:  true,
			expectedWarn: "Could not read user with the previous or the new encryption key, force-deleting",
			expectDM:     true,
		},
		{
			name:           "user unreadable with both keys is not notified when the force-delete fails",
			oldKeyData:     notEncryptedWithOldKey,
			newKeyErr:      unreadable,
			forceDelete:    true,
			forceDeleteErr: errors.New("error deleting the user"),
			expectedWarn:   "Failed to force-delete user during encryption key rotation",
		},
		{
			name:         "transient KV error leaves the user alone",
			oldKeyData:   func(*testing.T) []byte { return nil },
			oldKeyKVErr:  &model.AppError{Message: "KVGet failed"},
			newKeyErr:    kvErr,
			expectedWarn: "Could not load user during encryption key rotation, skipping",
		},
		{
			name:         "user unreadable with the old key but not loaded with the new key for another reason is left alone",
			oldKeyData:   notEncryptedWithOldKey,
			newKeyErr:    kvErr,
			expectedWarn: "Could not load user during encryption key rotation, skipping",
		},
		{
			name:         "user removed meanwhile is skipped",
			oldKeyData:   func(*testing.T) []byte { return nil },
			newKeyErr:    store.ErrNotFound,
			expectedWarn: "Could not load user during encryption key rotation, skipping",
		},
		{
			name:           "user that cannot be re-encrypted is force-deleted and notified",
			oldKeyData:     encryptedWithOldKey,
			storeUserFails: true,
			forceDelete:    true,
			expectedWarn:   "Failed to re-encrypt user data with new key, force-deleting",
			expectDM:       true,
		},
		{
			name:           "user that cannot be re-encrypted is not notified when the force-delete fails",
			oldKeyData:     encryptedWithOldKey,
			storeUserFails: true,
			forceDelete:    true,
			forceDeleteErr: errors.New("error deleting the user"),
			expectedWarn:   "Failed to force-delete user during encryption key rotation",
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockStore := mock_store.NewMockStore(ctrl)
			mockStore.EXPECT().LoadUserIndex().Return(store.UserIndex{
				{MattermostUserID: rotationMMUserID, RemoteID: "remoteID"},
			}, nil)
			if tc.storeUserFails {
				mockStore.EXPECT().StoreUser(gomock.Any()).Return(errors.New("error storing the user"))
			} else {
				// The user is not re-encrypted: it cannot be loaded with the previous key.
				if tc.loadsWithNewKey {
					mockStore.EXPECT().LoadUser(rotationMMUserID).Return(&store.User{MattermostUserID: rotationMMUserID}, nil)
				} else {
					mockStore.EXPECT().LoadUser(rotationMMUserID).Return(nil, tc.newKeyErr)
				}
			}
			if tc.forceDelete {
				mockStore.EXPECT().ForceDeleteUser(rotationMMUserID).Return(tc.forceDeleteErr)
			}

			mockAPI := &plugintest.API{}
			mockAPI.On("LogInfo", mock.Anything, mock.Anything, mock.Anything).Return()
			mockAPI.On("LogDebug", mock.Anything, mock.Anything, mock.Anything).Return()
			mockAPI.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
			mockAPI.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
			mockAPI.On("KVGet", rotationUserKey).Return(tc.oldKeyData(t), tc.oldKeyKVErr)

			p := &Plugin{envLock: &sync.RWMutex{}}
			p.SetAPI(mockAPI)
			fakeBot := &rotationBot{}
			e := &Env{
				Env: engine.Env{Dependencies: &engine.Dependencies{Store: mockStore}},
				bot: fakeBot,
			}

			p.reEncryptUserData(e, rotationOldKey)

			if tc.expectedWarn != "" {
				require.True(t, warned(mockAPI, tc.expectedWarn), "expected warning %q", tc.expectedWarn)
			} else {
				mockAPI.AssertNotCalled(t, "LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			}
			if tc.expectDM {
				require.Equal(t, []string{rotationMMUserID}, fakeBot.dms)
			} else {
				require.Empty(t, fakeBot.dms)
			}
		})
	}
}

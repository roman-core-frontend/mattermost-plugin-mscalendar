// Copyright (c) 2019-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package plugin

import (
	"encoding/json"
	"errors"
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

func TestReEncryptUserDataNotifiesOnlyAfterForceDelete(t *testing.T) {
	tcs := []struct {
		name           string
		storedUser     func(t *testing.T) []byte
		storeUserFails bool
		forceDeleteErr error
		expectedWarn   string
		expectDM       bool
	}{
		{
			name:         "user unreadable with the old key is force-deleted and notified",
			storedUser:   func(*testing.T) []byte { return []byte("not encrypted with the old key") },
			expectedWarn: "Could not decrypt user with previous encryption key, force-deleting",
			expectDM:     true,
		},
		{
			name:           "user unreadable with the old key is not notified when the force-delete fails",
			storedUser:     func(*testing.T) []byte { return []byte("not encrypted with the old key") },
			forceDeleteErr: errors.New("error deleting the user"),
			expectedWarn:   "Failed to force-delete user during encryption key rotation",
			expectDM:       false,
		},
		{
			name:           "user that cannot be re-encrypted is force-deleted and notified",
			storedUser:     encryptedWithOldKey,
			storeUserFails: true,
			expectedWarn:   "Failed to re-encrypt user data with new key, force-deleting",
			expectDM:       true,
		},
		{
			name:           "user that cannot be re-encrypted is not notified when the force-delete fails",
			storedUser:     encryptedWithOldKey,
			storeUserFails: true,
			forceDeleteErr: errors.New("error deleting the user"),
			expectedWarn:   "Failed to force-delete user during encryption key rotation",
			expectDM:       false,
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
			}
			mockStore.EXPECT().ForceDeleteUser(rotationMMUserID).Return(tc.forceDeleteErr)

			mockAPI := &plugintest.API{}
			mockAPI.On("LogInfo", mock.Anything, mock.Anything, mock.Anything).Return()
			mockAPI.On("LogWarn", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()
			mockAPI.On("KVGet", rotationUserKey).Return(tc.storedUser(t), nil)

			p := &Plugin{envLock: &sync.RWMutex{}}
			p.SetAPI(mockAPI)
			fakeBot := &rotationBot{}
			e := &Env{
				Env: engine.Env{Dependencies: &engine.Dependencies{Store: mockStore}},
				bot: fakeBot,
			}

			p.reEncryptUserData(e, rotationOldKey)

			mockAPI.AssertCalled(t, "LogWarn", tc.expectedWarn, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			if tc.expectDM {
				require.Equal(t, []string{rotationMMUserID}, fakeBot.dms)
			} else {
				require.Empty(t, fakeBot.dms)
			}
		})
	}
}

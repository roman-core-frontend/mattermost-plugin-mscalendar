// Copyright (c) 2019-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package command

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-mscalendar/calendar/config"
	"github.com/mattermost/mattermost-plugin-mscalendar/calendar/engine/mock_engine"
	"github.com/mattermost/mattermost-plugin-mscalendar/calendar/utils/kvstore"
)

func TestUnreadableConnectionMessage(t *testing.T) {
	jsonErr := json.Unmarshal([]byte("not json"), &struct{}{})
	require.Error(t, jsonErr)
	unreadableText := fmt.Sprintf(ConnectionUnreadableText, config.Provider.DisplayName, config.Provider.CommandTrigger)
	require.Contains(t, unreadableText, "can no longer be read, for example because the plugin encryption key was changed.")

	tcs := []struct {
		name           string
		loadErr        error
		expectedOutput string
		expectedError  string
	}{
		{
			name:           "stored user cannot be decrypted",
			loadErr:        fmt.Errorf("%w: cipher: message authentication failed", kvstore.ErrDecrypt),
			expectedOutput: unreadableText,
		},
		{
			name:           "stored user is not valid JSON",
			loadErr:        jsonErr,
			expectedOutput: unreadableText,
		},
		{
			name:          "failed KV read is a plain error",
			loadErr:       errors.New("failed plugin KVGet"),
			expectedError: "failed plugin KVGet",
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mscal := mock_engine.NewMockEngine(ctrl)
			mscal.EXPECT().GetRemoteUser("user_id").Return(nil, tc.loadErr).Times(1)

			command := Command{
				Context:   &plugin.Context{},
				Args:      &model.CommandArgs{Command: fmt.Sprintf("/%s today", config.Provider.CommandTrigger), UserId: "user_id"},
				ChannelID: "channel_id",
				Config:    &config.Config{PluginURL: "http://localhost"},
				Engine:    mscal,
			}

			out, _, err := command.Handle()
			if tc.expectedError != "" {
				require.ErrorContains(t, err, tc.expectedError)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.expectedOutput, out)
		})
	}
}

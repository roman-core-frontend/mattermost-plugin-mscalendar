// Copyright (c) 2019-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package command

import (
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
	ctrl := gomock.NewController(t)
	mscal := mock_engine.NewMockEngine(ctrl)
	mscal.EXPECT().GetRemoteUser("user_id").Return(nil, fmt.Errorf("%w: cipher: message authentication failed", kvstore.ErrDecrypt)).Times(1)

	command := Command{
		Context:   &plugin.Context{},
		Args:      &model.CommandArgs{Command: fmt.Sprintf("/%s today", config.Provider.CommandTrigger), UserId: "user_id"},
		ChannelID: "channel_id",
		Config:    &config.Config{PluginURL: "http://localhost"},
		Engine:    mscal,
	}

	out, _, err := command.Handle()
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf(ConnectionUnreadableText, config.Provider.DisplayName, config.Provider.CommandTrigger), out)
}

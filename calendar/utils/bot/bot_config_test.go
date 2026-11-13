// Copyright (c) 2019-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package bot

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestForwardsErrorsToAdmins(t *testing.T) {
	for level, expected := range map[string]bool{"": false, "none": false, "error": true, "warn": true, "info": true, "debug": true} {
		assert.Equal(t, expected, Config{AdminLogLevel: level}.ForwardsErrorsToAdmins(), level)
	}
}

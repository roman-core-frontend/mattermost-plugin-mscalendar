// Copyright (c) 2019-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseStatusRules(t *testing.T) {
	t.Run("empty config yields no rules", func(t *testing.T) {
		rules, err := ParseStatusRules("  ")
		require.NoError(t, err)
		assert.Nil(t, rules)
	})

	t.Run("valid rules are normalized", func(t *testing.T) {
		rules, err := ParseStatusRules(`[
			{"event_type": "outOfOffice", "status": "DND"},
			{"title_contains": "Отпуск", "status": "dnd", "emoji": ":palm_tree:", "text": "В отпуске"},
			{"event_type": "focusTime", "emoji": "headphones", "text": "Фокус: {title}"},
			{"title_contains": "Обед", "status": " None ", "emoji": "hamburger"}
		]`)
		require.NoError(t, err)
		require.Len(t, rules, 4)
		assert.Equal(t, StatusRuleStatusDnd, rules[0].Status)
		assert.True(t, rules[0].ChangesStatus())
		assert.Equal(t, StatusRuleStatusDnd, rules[0].StatusOverride())
		assert.Equal(t, "palm_tree", rules[1].Emoji)

		assert.Equal(t, "", rules[2].Status, "an omitted status uses the user's settings")
		assert.True(t, rules[2].ChangesStatus())
		assert.Equal(t, "", rules[2].StatusOverride())

		assert.Equal(t, StatusRuleStatusNoChange, rules[3].Status, "only an explicit none leaves the status unchanged")
		assert.False(t, rules[3].ChangesStatus())
		assert.Equal(t, "", rules[3].StatusOverride())
	})

	t.Run("malformed JSON yields no rules", func(t *testing.T) {
		rules, err := ParseStatusRules(`{"event_type": "outOfOffice"}`)
		assert.Error(t, err)
		assert.Empty(t, rules)
	})

	t.Run("invalid rules are skipped, valid ones are kept", func(t *testing.T) {
		rules, err := ParseStatusRules(`[
			{"status": "dnd"},
			{"event_type": "outOfOffice", "status": "dnd"},
			{"event_type": "outOfOffice", "title_contains": "x", "status": "dnd"},
			{"event_type": "focusTime", "status": "busy"}
		]`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "status rule #1 skipped")
		assert.Contains(t, err.Error(), "status rule #3 skipped")
		assert.Contains(t, err.Error(), "status rule #4 skipped")
		require.Len(t, rules, 1)
		assert.Equal(t, "outOfOffice", rules[0].EventType)
	})
}

func TestFindStatusRule(t *testing.T) {
	rules, err := ParseStatusRules(`[
		{"event_type": "outOfOffice", "status": "dnd"},
		{"title_contains": "отпуск", "status": "dnd", "emoji": "palm_tree", "text": "В отпуске"},
		{"title_contains": "Отпуск", "status": "away"}
	]`)
	require.NoError(t, err)
	assert.Nil(t, FindStatusRule([]StatusRule{}, "outOfOffice", ""))

	assert.Equal(t, &rules[0], FindStatusRule(rules, "outOfOffice", "Отпуск"), "first matching rule wins")
	assert.Equal(t, &rules[1], FindStatusRule(rules, "default", "ОТПУСК до 20-го"), "title match is case-insensitive")
	assert.Equal(t, &rules[0], FindStatusRule(rules, "OUTOFOFFICE", ""), "type match is case-insensitive")
	assert.Nil(t, FindStatusRule(rules, "default", "Daily standup"))
	assert.Nil(t, FindStatusRule(nil, "outOfOffice", "Отпуск"))
}

func TestStatusRuleCustomStatusText(t *testing.T) {
	assert.Equal(t, "Фокус: Code review", (&StatusRule{Text: "Фокус: {title}"}).CustomStatusText("Code review"))
	assert.Equal(t, "В отпуске", (&StatusRule{Text: "В отпуске"}).CustomStatusText("Отпуск"))
	assert.Equal(t, "Отпуск", (&StatusRule{}).CustomStatusText("Отпуск"))
}

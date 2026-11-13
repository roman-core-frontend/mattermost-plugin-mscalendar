// Copyright (c) 2019-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package engine

import (
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-mscalendar/calendar/config"
	"github.com/mattermost/mattermost-plugin-mscalendar/calendar/engine/mock_plugin_api"
	"github.com/mattermost/mattermost-plugin-mscalendar/calendar/engine/views"
	"github.com/mattermost/mattermost-plugin-mscalendar/calendar/remote"
	"github.com/mattermost/mattermost-plugin-mscalendar/calendar/store"
	"github.com/mattermost/mattermost-plugin-mscalendar/calendar/store/mock_store"
	"github.com/mattermost/mattermost-plugin-mscalendar/calendar/utils/bot/mock_bot"
)

func testStatusRules(t *testing.T) []config.StatusRule {
	rules, err := config.ParseStatusRules(`[
		{"event_type": "outOfOffice", "status": "dnd"},
		{"title_contains": "Отпуск", "status": "dnd", "emoji": "palm_tree", "text": "В отпуске"},
		{"event_type": "focusTime", "status": "away", "emoji": "headphones", "text": "Фокус: {title}"},
		{"title_contains": "Обед", "status": "none", "emoji": "hamburger"}
	]`)
	require.NoError(t, err)
	return rules
}

// testEvent creates an event of the user. Events with attendees are organized by someone else
// unless isOrganizer is set afterwards.
func testEvent(subject, eventType, showAs string, attendees int) *remote.Event {
	now := time.Now().UTC()
	e := &remote.Event{
		ID:        subject,
		Subject:   subject,
		EventType: eventType,
		ShowAs:    showAs,
		Start:     remote.NewDateTime(now.Add(-time.Minute), "UTC"),
		End:       remote.NewDateTime(now.Add(time.Hour), "UTC"),
	}
	for i := 0; i < attendees; i++ {
		e.Attendees = append(e.Attendees, &remote.Attendee{})
	}
	return e
}

func eventAt(subject, eventType string, start, end time.Time) *remote.Event {
	return &remote.Event{
		ID:        subject,
		Subject:   subject,
		EventType: eventType,
		ShowAs:    "busy",
		Start:     remote.NewDateTime(start, "UTC"),
		End:       remote.NewDateTime(end, "UTC"),
	}
}

func eventSubjects(events []*remote.Event) []string {
	result := []string{}
	for _, e := range events {
		result = append(result, e.Subject)
	}
	return result
}

func TestEventRules(t *testing.T) {
	rules := testStatusRules(t)

	t.Run("own events match", func(t *testing.T) {
		assert.Equal(t, "dnd", statusRuleOf(rules, testEvent("Не в офисе", "outOfOffice", "busy", 0)).Status)
		assert.Equal(t, "palm_tree", statusRuleOf(rules, testEvent("ОТПУСК на море", "default", "free", 0)).Emoji)

		organized := testEvent("Отпуск команды", "default", "busy", 3)
		organized.IsOrganizer = true
		assert.NotNil(t, statusRuleOf(rules, organized))
	})

	t.Run("invitations from others do not match", func(t *testing.T) {
		assert.Nil(t, statusRuleOf(rules, testEvent("Отпуск Пети", "default", "free", 5)))
	})

	t.Run("Microsoft Graph out-of-office events match outOfOffice rules", func(t *testing.T) {
		assert.NotNil(t, statusRuleOf(rules, testEvent("Away", "", "oof", 0)))
	})

	t.Run("no rules", func(t *testing.T) {
		assert.Nil(t, statusRuleOf(nil, testEvent("Отпуск", "default", "free", 0)))
	})
}

func TestFilterBusyAndAttendeeEvents(t *testing.T) {
	rules := testStatusRules(t)

	cancelled := testEvent("Cancelled meeting", "default", "busy", 2)
	cancelled.IsCancelled = true
	cancelledOOO := testEvent("Cancelled OOO", "outOfOffice", "busy", 0)
	cancelledOOO.IsCancelled = true
	allDayVacation := testEvent("Отпуск", "default", "free", 0)
	allDayVacation.IsAllDay = true

	events := []*remote.Event{
		testEvent("Meeting", "default", "busy", 2),
		testEvent("Solo busy block", "default", "busy", 0),
		testEvent("Free meeting", "default", "free", 2),
		cancelled,
		cancelledOOO,
		testEvent("Не в офисе", "outOfOffice", "busy", 0),
		allDayVacation,
		testEvent("Deep work", "focusTime", "busy", 0),
		testEvent("Отпуск Пети", "default", "free", 5),
	}

	t.Run("with rules", func(t *testing.T) {
		assert.Equal(t,
			[]string{"Meeting", "Не в офисе", "Отпуск", "Deep work"},
			eventSubjects(filterBusyAndAttendeeEvents(events, rules)))
	})

	t.Run("without rules the behavior is unchanged", func(t *testing.T) {
		assert.Equal(t, []string{"Meeting"}, eventSubjects(filterBusyAndAttendeeEvents(events, nil)))
	})
}

func TestFilterStatusChangingEvents(t *testing.T) {
	rules := testStatusRules(t)
	events := []*remote.Event{
		testEvent("Meeting", "default", "busy", 2),
		testEvent("Обед", "default", "busy", 0),
		testEvent("Отпуск", "default", "free", 0),
	}
	assert.Equal(t, []string{"Meeting", "Отпуск"}, eventSubjects(filterStatusChangingEvents(events, rules)))
}

func TestCustomStatusForEvent(t *testing.T) {
	rules := testStatusRules(t)

	for name, tc := range map[string]struct {
		event         *remote.Event
		expectedEmoji string
		expectedText  string
	}{
		"meeting uses event title":           {testEvent("Daily standup", "default", "busy", 3), "calendar", "Daily standup"},
		"empty title falls back":             {testEvent("  ", "default", "busy", 3), "calendar", "In a meeting"},
		"title rule replaces text and emoji": {testEvent("Отпуск на море", "default", "free", 0), "palm_tree", "В отпуске"},
		"type rule with {title}":             {testEvent("Code review", "focusTime", "busy", 0), "headphones", "Фокус: Code review"},
		"type rule without text uses title":  {testEvent("Не в офисе", "outOfOffice", "busy", 0), "calendar", "Не в офисе"},
		"invitation is a regular meeting":    {testEvent("Отпуск Пети", "default", "busy", 5), "calendar", "Отпуск Пети"},
	} {
		t.Run(name, func(t *testing.T) {
			emoji, text := customStatusForEvent(tc.event, statusRuleOf(rules, tc.event))
			assert.Equal(t, tc.expectedEmoji, emoji)
			assert.Equal(t, tc.expectedText, text)
		})
	}
}

func TestBusyStatusForEvent(t *testing.T) {
	rules := testStatusRules(t)
	dndUser := &store.User{Settings: store.Settings{UpdateStatusFromOptions: store.DNDStatusOption}}
	awayUser := &store.User{Settings: store.Settings{UpdateStatusFromOptions: store.AwayStatusOption}}

	assert.Equal(t, model.StatusAway, busyStatusForEvent(awayUser, statusRuleOf(rules, testEvent("Meeting", "default", "busy", 2))))
	assert.Equal(t, model.StatusDnd, busyStatusForEvent(awayUser, statusRuleOf(rules, testEvent("Не в офисе", "outOfOffice", "busy", 0))))
	assert.Equal(t, model.StatusAway, busyStatusForEvent(dndUser, statusRuleOf(rules, testEvent("Deep work", "focusTime", "busy", 0))))
	assert.Equal(t, model.StatusDnd, busyStatusForEvent(dndUser, nil))
}

func TestIsPluginBusyStatus(t *testing.T) {
	user := &store.User{Settings: store.Settings{UpdateStatusFromOptions: store.DNDStatusOption}}
	assert.True(t, user.IsPluginBusyStatus(model.StatusDnd), "falls back to the status from the settings")
	assert.False(t, user.IsPluginBusyStatus(model.StatusAway), "a status set manually is not the plugin's")

	user.BusyStatus = model.StatusAway
	assert.True(t, user.IsPluginBusyStatus(model.StatusAway), "status set by a rule")
	assert.False(t, user.IsPluginBusyStatus(model.StatusDnd))
}

func TestIsCustomStatusActive(t *testing.T) {
	assert.False(t, isCustomStatusActive(nil))
	assert.True(t, isCustomStatusActive(&model.CustomStatus{Text: "No expiry"}))
	assert.True(t, isCustomStatusActive(&model.CustomStatus{Text: "Later", ExpiresAt: time.Now().Add(time.Hour)}))
	assert.False(t, isCustomStatusActive(&model.CustomStatus{Text: "Expired", ExpiresAt: time.Now().Add(-time.Minute)}))
}

func TestGetMergedEventsWithRules(t *testing.T) {
	rules := testStatusRules(t)
	now := time.Now().UTC().Truncate(time.Minute)

	t.Run("a shorter event inside a longer one does not cut it, and originals are not modified", func(t *testing.T) {
		long := eventAt("Long", "default", now.Add(-time.Hour), now.Add(3*time.Hour))
		short := eventAt("Short", "default", now, now.Add(time.Hour))
		merged := getMergedEvents([]*remote.Event{long, short}, rules)
		require.Len(t, merged, 1)
		assert.Equal(t, now.Add(3*time.Hour), merged[0].End.Time())
		assert.Equal(t, now.Add(3*time.Hour), long.End.Time())
		assert.NotSame(t, long, merged[0])
	})

	t.Run("adjacent events with different rules are not merged", func(t *testing.T) {
		focus := eventAt("Deep work", "focusTime", now.Add(-time.Hour), now)
		meeting := eventAt("Meeting", "default", now, now.Add(time.Hour))
		merged := getMergedEvents([]*remote.Event{focus, meeting}, rules)
		assert.Equal(t, []string{"Deep work", "Meeting"}, eventSubjects(merged))
	})
}

func TestCurrentEvent(t *testing.T) {
	now := time.Now().UTC()
	ooo := eventAt("Не в офисе", "outOfOffice", now.Add(-10*time.Hour), now.Add(10*time.Hour))
	meeting := eventAt("Meeting", "default", now.Add(-5*time.Minute), now.Add(time.Hour))
	upcoming := eventAt("Upcoming", "default", now.Add(5*time.Minute), now.Add(time.Hour))

	assert.Nil(t, currentEvent(nil))
	assert.Equal(t, "Meeting", currentEvent([]*remote.Event{ooo, meeting, upcoming}).Subject, "latest started event wins")
	assert.Equal(t, "Не в офисе", currentEvent([]*remote.Event{ooo, upcoming}).Subject)
	assert.Equal(t, "Upcoming", currentEvent([]*remote.Event{upcoming}).Subject)
}

func TestSetStatusOrAskUserKeepsLastStatus(t *testing.T) {
	newEngine := func(t *testing.T) (*mscalendar, *mock_store.MockStore, *mock_plugin_api.MockPluginAPI, *mock_bot.MockPoster) {
		ctrl := gomock.NewController(t)
		s := mock_store.NewMockStore(ctrl)
		papi := mock_plugin_api.NewMockPluginAPI(ctrl)
		poster := mock_bot.NewMockPoster(ctrl)
		m := &mscalendar{Env: Env{
			Config:       &config.Config{},
			Dependencies: &Dependencies{Store: s, PluginAPI: papi, Poster: poster},
		}}
		return m, s, papi, poster
	}
	events := []*remote.Event{testEvent("Meeting", "default", "busy", 2)}

	t.Run("start of the first busy event stores the manual status", func(t *testing.T) {
		m, s, papi, _ := newEngine(t)
		user := &store.User{MattermostUserID: "user_mm_id", Settings: store.Settings{UpdateStatusFromOptions: store.DNDStatusOption}}

		s.EXPECT().StoreUser(user).Return(nil)
		papi.EXPECT().UpdateMattermostUserStatus("user_mm_id", model.StatusAway).Return(nil, nil)

		require.NoError(t, m.setStatusOrAskUser(user, &model.Status{Status: model.StatusOnline, Manual: true}, events, false, model.StatusAway))
		assert.Equal(t, model.StatusOnline, user.LastStatus)
		assert.Equal(t, model.StatusAway, user.BusyStatus)
	})

	t.Run("switching between busy events keeps the status from before the first one", func(t *testing.T) {
		m, s, papi, _ := newEngine(t)
		user := &store.User{
			MattermostUserID: "user_mm_id",
			Settings:         store.Settings{UpdateStatusFromOptions: store.DNDStatusOption},
			ActiveEvents:     []string{"focus"},
			LastStatus:       model.StatusOnline,
			BusyStatus:       model.StatusAway,
		}

		s.EXPECT().StoreUser(user).Return(nil)
		papi.EXPECT().UpdateMattermostUserStatus("user_mm_id", model.StatusDnd).Return(nil, nil)

		require.NoError(t, m.setStatusOrAskUser(user, &model.Status{Status: model.StatusAway, Manual: true}, events, false, model.StatusDnd))
		assert.Equal(t, model.StatusOnline, user.LastStatus)
		assert.Equal(t, model.StatusDnd, user.BusyStatus)
	})

	t.Run("a status changed manually during a meeting is stored", func(t *testing.T) {
		m, s, papi, _ := newEngine(t)
		user := &store.User{
			MattermostUserID: "user_mm_id",
			Settings:         store.Settings{UpdateStatusFromOptions: store.DNDStatusOption},
			ActiveEvents:     []string{"meeting"},
			LastStatus:       model.StatusOnline,
			BusyStatus:       model.StatusDnd,
		}

		s.EXPECT().StoreUser(user).Return(nil)
		papi.EXPECT().UpdateMattermostUserStatus("user_mm_id", model.StatusDnd).Return(nil, nil)

		require.NoError(t, m.setStatusOrAskUser(user, &model.Status{Status: model.StatusAway, Manual: true}, events, false, model.StatusDnd))
		assert.Equal(t, model.StatusAway, user.LastStatus)
	})

	t.Run("end of the busy period restores the last status and clears the busy status", func(t *testing.T) {
		m, s, papi, _ := newEngine(t)
		user := &store.User{MattermostUserID: "user_mm_id", LastStatus: model.StatusOnline, BusyStatus: model.StatusAway, ActiveEvents: []string{"focus"}}

		s.EXPECT().StoreUser(user).Return(nil)
		papi.EXPECT().UpdateMattermostUserStatus("user_mm_id", model.StatusOnline).Return(nil, nil)

		require.NoError(t, m.setStatusOrAskUser(user, &model.Status{Status: model.StatusAway, Manual: true}, nil, true, model.StatusDnd))
		assert.Equal(t, "", user.LastStatus)
		assert.Equal(t, "", user.BusyStatus)
	})

	t.Run("confirmation carries the transition flags", func(t *testing.T) {
		m, s, _, poster := newEngine(t)
		user := &store.User{
			MattermostUserID: "user_mm_id",
			Settings:         store.Settings{GetConfirmation: true},
			ActiveEvents:     []string{"focus"},
			LastStatus:       model.StatusOnline,
			BusyStatus:       model.StatusAway,
		}

		s.EXPECT().StoreUser(user).Return(nil)
		poster.EXPECT().DMWithAttachments("user_mm_id", gomock.Any()).DoAndReturn(func(_ string, attachments ...*model.SlackAttachment) (string, error) {
			require.Len(t, attachments, 1)
			yes := attachments[0].Actions[0].Integration.Context
			assert.Equal(t, true, yes[views.StatusChangeContextIsBusy])
			assert.Equal(t, true, yes[views.StatusChangeContextKeepLastStatus])
			no := attachments[0].Actions[1].Integration.Context
			assert.NotContains(t, no, views.StatusChangeContextIsBusy)
			return "", nil
		})

		require.NoError(t, m.setStatusOrAskUser(user, &model.Status{Status: model.StatusAway, Manual: true}, events, false, model.StatusDnd))
		assert.Equal(t, model.StatusOnline, user.LastStatus)
		assert.Equal(t, model.StatusAway, user.BusyStatus, "changed only when the user confirms")
	})

	t.Run("end of the busy period with confirmation clears the busy status", func(t *testing.T) {
		m, s, _, poster := newEngine(t)
		user := &store.User{MattermostUserID: "user_mm_id", Settings: store.Settings{GetConfirmation: true}, BusyStatus: model.StatusAway, ActiveEvents: []string{"focus"}}

		s.EXPECT().StoreUser(user).Return(nil)
		poster.EXPECT().DMWithAttachments("user_mm_id", gomock.Any()).Return("", nil)

		require.NoError(t, m.setStatusOrAskUser(user, &model.Status{Status: model.StatusAway, Manual: true}, nil, true, model.StatusDnd))
		assert.Equal(t, "", user.BusyStatus)
	})
}

func TestSetStatusFromCalendarViewNextEventStarts(t *testing.T) {
	ctrl := gomock.NewController(t)
	s := mock_store.NewMockStore(ctrl)
	papi := mock_plugin_api.NewMockPluginAPI(ctrl)
	rules := testStatusRules(t)
	m := &mscalendar{Env: Env{
		Config:       &config.Config{StoredConfig: config.StoredConfig{ParsedStatusRules: rules}},
		Dependencies: &Dependencies{Store: s, PluginAPI: papi},
	}}

	now := time.Now().UTC().Truncate(time.Minute)
	focus := eventAt("Deep work", "focusTime", now.Add(-55*time.Minute), now.Add(5*time.Minute))
	meeting := eventAt("Meeting", "default", now.Add(5*time.Minute), now.Add(time.Hour))
	meeting.Attendees = []*remote.Attendee{{}}

	user := &store.User{
		MattermostUserID: "user_mm_id",
		Settings:         store.Settings{UpdateStatusFromOptions: store.DNDStatusOption},
		LastStatus:       model.StatusOnline,
		BusyStatus:       model.StatusAway,
	}

	// 5 minutes before the meeting: focus time is current, the meeting is already fetched.
	before := getMergedEvents(filterStatusChangingEvents([]*remote.Event{focus, meeting}, rules), rules)
	user.ActiveEvents = []string{eventHash(focus), currentEventMarker + eventHash(focus)}
	var stored []string
	s.EXPECT().StoreUserActiveEvents("user_mm_id", gomock.Any()).DoAndReturn(func(_ string, events []string) error {
		stored = events
		return nil
	})
	_, changed, err := m.setStatusFromCalendarView(user, &model.Status{Status: model.StatusAway, Manual: true}, before, rules)
	require.NoError(t, err)
	assert.False(t, changed, "focus time is still in progress")
	assert.Contains(t, stored, eventHash(meeting))

	// The meeting starts: focus time has ended and is no longer fetched, so no new event is seen,
	// but the current event changed and its status must be applied.
	user.ActiveEvents = stored
	after := getMergedEvents(filterStatusChangingEvents([]*remote.Event{meeting}, rules), rules)
	s.EXPECT().StoreUser(user).Return(nil)
	papi.EXPECT().UpdateMattermostUserStatus("user_mm_id", model.StatusDnd).Return(nil, nil)
	s.EXPECT().StoreUserActiveEvents("user_mm_id", gomock.Any()).Return(nil)
	_, changed, err = m.setStatusFromCalendarView(user, &model.Status{Status: model.StatusAway, Manual: true}, after, rules)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, model.StatusOnline, user.LastStatus, "the status from before focus time is kept")
	assert.Equal(t, model.StatusDnd, user.BusyStatus)
}

func TestRenderStatusChangeNotificationViewShowsCurrentEvent(t *testing.T) {
	now := time.Now().UTC()
	ooo := eventAt("Не в офисе", "outOfOffice", now.Add(-time.Hour), now.Add(10*time.Hour))

	attachment := views.RenderStatusChangeNotificationView(ooo, model.StatusAway, "url", true, false)
	assert.Contains(t, attachment.Text, "Не в офисе")
	assert.NotContains(t, attachment.Text, "no upcoming events")

	attachment = views.RenderStatusChangeNotificationView(ooo, model.StatusOnline, "url", false, false)
	assert.Contains(t, attachment.Text, "no upcoming events", "going back to the previous status shows no event")
}

func TestSetStatusFromCalendarViewManualStatusDuringFocus(t *testing.T) {
	ctrl := gomock.NewController(t)
	s := mock_store.NewMockStore(ctrl)
	rules := testStatusRules(t)
	m := &mscalendar{Env: Env{
		Config:       &config.Config{StoredConfig: config.StoredConfig{ParsedStatusRules: rules}},
		Dependencies: &Dependencies{Store: s},
	}}

	now := time.Now().UTC().Truncate(time.Minute)
	focus := eventAt("Deep work", "focusTime", now.Add(-time.Hour), now.Add(-time.Minute))
	meeting := eventAt("Meeting", "default", now.Add(-time.Minute), now.Add(time.Hour))
	meeting.Attendees = []*remote.Attendee{{}}

	// Focus time set "away", then the user set "online" manually; the meeting was fetched before it started.
	user := &store.User{
		MattermostUserID: "user_mm_id",
		Settings:         store.Settings{UpdateStatusFromOptions: store.DNDStatusOption},
		BusyStatus:       model.StatusAway,
		ActiveEvents:     []string{eventHash(focus), eventHash(meeting), currentEventMarker + eventHash(focus)},
	}

	// The meeting starts: the manual status is kept, and the new current event is remembered.
	s.EXPECT().StoreUserActiveEvents("user_mm_id", []string{eventHash(meeting), currentEventMarker + eventHash(meeting)}).Return(nil)
	_, changed, err := m.setStatusFromCalendarView(user, &model.Status{Status: model.StatusOnline, Manual: true}, []*remote.Event{meeting}, rules)
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestStatusRuleWithoutNormalize(t *testing.T) {
	rules := []config.StatusRule{{TitleContains: "Обед", Status: "away"}}
	assert.Nil(t, config.FindStatusRule(rules, "default", "Meeting"))
	assert.NotNil(t, config.FindStatusRule(rules, "default", "обед с командой"))
	assert.Nil(t, config.FindStatusRule([]config.StatusRule{{Status: "away"}}, "default", "Meeting"), "a rule without a condition matches nothing")
}

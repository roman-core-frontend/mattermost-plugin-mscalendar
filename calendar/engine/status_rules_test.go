// Copyright (c) 2019-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package engine

import (
	"errors"
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
		assert.Equal(t, "dnd", statusRuleOf(rules, testEvent("Не в офисе", remote.EventTypeOutOfOffice, "busy", 0)).Status)
		assert.Equal(t, "palm_tree", statusRuleOf(rules, testEvent("ОТПУСК на море", remote.EventTypeDefault, "free", 0)).Emoji)

		organized := testEvent("Отпуск команды", remote.EventTypeDefault, "busy", 3)
		organized.IsOrganizer = true
		assert.NotNil(t, statusRuleOf(rules, organized))
	})

	t.Run("invitations from others do not match", func(t *testing.T) {
		assert.Nil(t, statusRuleOf(rules, testEvent("Отпуск Пети", remote.EventTypeDefault, "free", 5)))
	})

	t.Run("Microsoft Graph out-of-office events match outOfOffice rules", func(t *testing.T) {
		assert.NotNil(t, statusRuleOf(rules, testEvent("Away", "", remote.ShowAsOutOfOffice, 0)))
	})

	t.Run("no rules", func(t *testing.T) {
		assert.Nil(t, statusRuleOf(nil, testEvent("Отпуск", remote.EventTypeDefault, "free", 0)))
	})
}

func TestFilterBusyAndAttendeeEvents(t *testing.T) {
	rules := testStatusRules(t)

	cancelled := testEvent("Cancelled meeting", remote.EventTypeDefault, "busy", 2)
	cancelled.IsCancelled = true
	cancelledOOO := testEvent("Cancelled OOO", remote.EventTypeOutOfOffice, "busy", 0)
	cancelledOOO.IsCancelled = true
	allDayVacation := testEvent("Отпуск", remote.EventTypeDefault, "free", 0)
	allDayVacation.IsAllDay = true

	events := []*remote.Event{
		testEvent("Meeting", remote.EventTypeDefault, "busy", 2),
		testEvent("Solo busy block", remote.EventTypeDefault, "busy", 0),
		testEvent("Free meeting", remote.EventTypeDefault, "free", 2),
		cancelled,
		cancelledOOO,
		testEvent("Не в офисе", remote.EventTypeOutOfOffice, "busy", 0),
		allDayVacation,
		testEvent("Deep work", remote.EventTypeFocusTime, "busy", 0),
		testEvent("Отпуск Пети", remote.EventTypeDefault, "free", 5),
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
		testEvent("Meeting", remote.EventTypeDefault, "busy", 2),
		testEvent("Обед", remote.EventTypeDefault, "busy", 0),
		testEvent("Отпуск", remote.EventTypeDefault, "free", 0),
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
		"meeting uses event title":           {testEvent("Daily standup", remote.EventTypeDefault, "busy", 3), "calendar", "Daily standup"},
		"empty title falls back":             {testEvent("  ", remote.EventTypeDefault, "busy", 3), "calendar", "In a meeting"},
		"title rule replaces text and emoji": {testEvent("Отпуск на море", remote.EventTypeDefault, "free", 0), "palm_tree", "В отпуске"},
		"type rule with {title}":             {testEvent("Code review", remote.EventTypeFocusTime, "busy", 0), "headphones", "Фокус: Code review"},
		"type rule without text uses title":  {testEvent("Не в офисе", remote.EventTypeOutOfOffice, "busy", 0), "calendar", "Не в офисе"},
		"invitation is a regular meeting":    {testEvent("Отпуск Пети", remote.EventTypeDefault, "busy", 5), "calendar", "Отпуск Пети"},
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

	assert.Equal(t, model.StatusAway, busyStatusForEvent(awayUser, statusRuleOf(rules, testEvent("Meeting", remote.EventTypeDefault, "busy", 2))))
	assert.Equal(t, model.StatusDnd, busyStatusForEvent(awayUser, statusRuleOf(rules, testEvent("Не в офисе", remote.EventTypeOutOfOffice, "busy", 0))))
	assert.Equal(t, model.StatusAway, busyStatusForEvent(dndUser, statusRuleOf(rules, testEvent("Deep work", remote.EventTypeFocusTime, "busy", 0))))
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
		long := eventAt("Long", remote.EventTypeDefault, now.Add(-time.Hour), now.Add(3*time.Hour))
		short := eventAt("Short", remote.EventTypeDefault, now, now.Add(time.Hour))
		merged := getMergedEvents([]*remote.Event{long, short}, rules)
		require.Len(t, merged, 1)
		assert.Equal(t, now.Add(3*time.Hour), merged[0].End.Time())
		assert.Equal(t, now.Add(3*time.Hour), long.End.Time())
		assert.NotSame(t, long, merged[0])
	})

	t.Run("adjacent events with different rules are not merged", func(t *testing.T) {
		focus := eventAt("Deep work", remote.EventTypeFocusTime, now.Add(-time.Hour), now)
		meeting := eventAt("Meeting", remote.EventTypeDefault, now, now.Add(time.Hour))
		merged := getMergedEvents([]*remote.Event{focus, meeting}, rules)
		assert.Equal(t, []string{"Deep work", "Meeting"}, eventSubjects(merged))
	})
}

func TestCurrentEvent(t *testing.T) {
	rules := testStatusRules(t)
	now := time.Now().UTC()
	ooo := eventAt("Не в офисе", remote.EventTypeOutOfOffice, now.Add(-10*time.Hour), now.Add(10*time.Hour))
	vacation := eventAt("Отпуск", remote.EventTypeDefault, now.Add(-10*time.Hour), now.Add(10*time.Hour))
	focus := eventAt("Deep work", remote.EventTypeFocusTime, now.Add(-2*time.Hour), now.Add(time.Hour))
	meeting := eventAt("Time to focus", remote.EventTypeDefault, now.Add(-5*time.Minute), now.Add(time.Hour))
	meeting.Attendees = []*remote.Attendee{{}}
	meeting.IsOrganizer = true
	other := eventAt("Standup", remote.EventTypeDefault, now.Add(-3*time.Minute), now.Add(time.Hour))
	upcoming := eventAt("Upcoming", remote.EventTypeDefault, now.Add(5*time.Minute), now.Add(time.Hour))

	assert.Nil(t, currentEvent(nil, rules))
	assert.Equal(t, "Не в офисе", currentEvent([]*remote.Event{ooo, meeting, upcoming}, rules).Subject,
		"an event matched by a rule wins over a later meeting")
	assert.Equal(t, "Не в офисе", currentEvent([]*remote.Event{vacation, ooo, focus}, rules).Subject,
		"the rule higher in the list wins")
	assert.Equal(t, "Standup", currentEvent([]*remote.Event{meeting, other}, rules).Subject,
		"among events without rules the latest started wins")
	assert.Equal(t, "Standup", currentEvent([]*remote.Event{meeting, other}, nil).Subject)
	assert.Equal(t, "Upcoming", currentEvent([]*remote.Event{upcoming}, rules).Subject)
	assert.Equal(t, "Не в офисе", currentEvent([]*remote.Event{ooo, upcoming}, rules).Subject)
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
	current := testEvent("Meeting", remote.EventTypeDefault, "busy", 2)

	t.Run("start of the first busy event stores the manual status", func(t *testing.T) {
		m, s, papi, _ := newEngine(t)
		user := &store.User{MattermostUserID: "user_mm_id", Settings: store.Settings{UpdateStatusFromOptions: store.DNDStatusOption}}

		s.EXPECT().StoreUser(user).Return(nil)
		papi.EXPECT().UpdateMattermostUserStatus("user_mm_id", model.StatusAway).Return(nil, nil)

		require.NoError(t, m.setStatusOrAskUser(user, &model.Status{Status: model.StatusOnline, Manual: true}, current, false, model.StatusAway))
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

		require.NoError(t, m.setStatusOrAskUser(user, &model.Status{Status: model.StatusAway, Manual: true}, current, false, model.StatusDnd))
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

		require.NoError(t, m.setStatusOrAskUser(user, &model.Status{Status: model.StatusAway, Manual: true}, current, false, model.StatusDnd))
		assert.Equal(t, model.StatusAway, user.LastStatus)
	})

	t.Run("end of the busy period restores the last status and clears the busy status", func(t *testing.T) {
		m, s, papi, _ := newEngine(t)
		user := &store.User{MattermostUserID: "user_mm_id", LastStatus: model.StatusOnline, BusyStatus: model.StatusAway, ActiveEvents: []string{"focus"}}

		// The busy period is cleared on the reloaded user: the user passed in is not stored.
		gomock.InOrder(
			papi.EXPECT().UpdateMattermostUserStatus("user_mm_id", model.StatusOnline).Return(nil, nil),
			s.EXPECT().ClearUserBusyPeriod("user_mm_id").Return(nil),
		)

		require.NoError(t, m.setStatusOrAskUser(user, &model.Status{Status: model.StatusAway, Manual: true}, nil, true, model.StatusDnd))
		assert.Equal(t, "", user.LastStatus)
		assert.Equal(t, "", user.BusyStatus)
		assert.Empty(t, user.ActiveEvents)
	})

	t.Run("a failed status change leaves the stored user unchanged", func(t *testing.T) {
		m, _, papi, _ := newEngine(t)
		user := &store.User{
			MattermostUserID: "user_mm_id",
			Settings:         store.Settings{UpdateStatusFromOptions: store.DNDStatusOption},
			ActiveEvents:     []string{"focus"},
			LastStatus:       model.StatusOnline,
			BusyStatus:       model.StatusAway,
		}
		before := *user

		// No StoreUser call is expected.
		papi.EXPECT().UpdateMattermostUserStatus("user_mm_id", model.StatusDnd).Return(nil, errors.New("update failed"))

		require.Error(t, m.setStatusOrAskUser(user, &model.Status{Status: model.StatusAway, Manual: true}, current, false, model.StatusDnd))
		assert.Equal(t, before, *user)
	})

	t.Run("a failed status restore leaves the stored user unchanged", func(t *testing.T) {
		m, _, papi, _ := newEngine(t)
		user := &store.User{MattermostUserID: "user_mm_id", LastStatus: model.StatusOnline, BusyStatus: model.StatusAway, ActiveEvents: []string{"focus"}}
		before := *user

		// No StoreUser call is expected.
		papi.EXPECT().UpdateMattermostUserStatus("user_mm_id", model.StatusOnline).Return(nil, errors.New("update failed"))

		require.Error(t, m.setStatusOrAskUser(user, &model.Status{Status: model.StatusAway, Manual: true}, nil, true, model.StatusDnd))
		assert.Equal(t, before, *user)
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
			assert.Contains(t, attachments[0].Text, "Meeting", "the current event passed in is shown")
			yes := attachments[0].Actions[0].Integration.Context
			assert.Equal(t, true, yes[views.StatusChangeContextIsBusy])
			assert.Equal(t, true, yes[views.StatusChangeContextKeepLastStatus])
			no := attachments[0].Actions[1].Integration.Context
			assert.NotContains(t, no, views.StatusChangeContextIsBusy)
			return "", nil
		})

		require.NoError(t, m.setStatusOrAskUser(user, &model.Status{Status: model.StatusAway, Manual: true}, current, false, model.StatusDnd))
		assert.Equal(t, model.StatusOnline, user.LastStatus)
		assert.Equal(t, model.StatusAway, user.BusyStatus, "changed only when the user confirms")
	})

	t.Run("a failed confirmation at the end of the busy period leaves the stored user unchanged", func(t *testing.T) {
		m, _, _, poster := newEngine(t)
		user := &store.User{
			MattermostUserID: "user_mm_id",
			Settings:         store.Settings{GetConfirmation: true},
			LastStatus:       model.StatusOnline,
			BusyStatus:       model.StatusAway,
			ActiveEvents:     []string{"focus"},
		}
		before := *user

		// No StoreUser call is expected.
		poster.EXPECT().DMWithAttachments("user_mm_id", gomock.Any()).Return("", errors.New("DM failed"))

		require.Error(t, m.setStatusOrAskUser(user, &model.Status{Status: model.StatusAway, Manual: true}, nil, true, model.StatusDnd))
		assert.Equal(t, before, *user)
	})

	t.Run("end of the busy period with confirmation clears the busy status", func(t *testing.T) {
		m, s, _, poster := newEngine(t)
		user := &store.User{MattermostUserID: "user_mm_id", Settings: store.Settings{GetConfirmation: true}, BusyStatus: model.StatusAway, ActiveEvents: []string{"focus"}}

		gomock.InOrder(
			poster.EXPECT().DMWithAttachments("user_mm_id", gomock.Any()).DoAndReturn(func(_ string, attachments ...*model.SlackAttachment) (string, error) {
				require.Len(t, attachments, 1)
				yes := attachments[0].Actions[0].Integration.Context
				assert.Equal(t, model.StatusOnline, yes["change_to"], "the confirmation carries the status to go back to")
				return "", nil
			}),
			s.EXPECT().ClearUserBusyPeriod("user_mm_id").Return(nil),
		)

		require.NoError(t, m.setStatusOrAskUser(user, &model.Status{Status: model.StatusAway, Manual: true}, nil, true, model.StatusDnd))
		assert.Equal(t, "", user.BusyStatus)
		assert.Equal(t, "", user.LastStatus)
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
	focus := eventAt("Deep work", remote.EventTypeFocusTime, now.Add(-55*time.Minute), now.Add(5*time.Minute))
	meeting := eventAt("Meeting", remote.EventTypeDefault, now.Add(5*time.Minute), now.Add(time.Hour))
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
	ooo := eventAt("Не в офисе", remote.EventTypeOutOfOffice, now.Add(-time.Hour), now.Add(10*time.Hour))

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
	focus := eventAt("Deep work", remote.EventTypeFocusTime, now.Add(-time.Hour), now.Add(-time.Minute))
	meeting := eventAt("Meeting", remote.EventTypeDefault, now.Add(-time.Minute), now.Add(time.Hour))
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
	assert.Nil(t, config.FindStatusRule(rules, remote.EventTypeDefault, "Meeting"))
	assert.NotNil(t, config.FindStatusRule(rules, remote.EventTypeDefault, "обед с командой"))
	assert.Nil(t, config.FindStatusRule([]config.StatusRule{{Status: "away"}}, remote.EventTypeDefault, "Meeting"), "a rule without a condition matches nothing")
}

func TestSetStatusFromCalendarViewEndOfBusyPeriod(t *testing.T) {
	newEngine := func(t *testing.T) (*mscalendar, *mock_store.MockStore, *mock_plugin_api.MockPluginAPI) {
		ctrl := gomock.NewController(t)
		s := mock_store.NewMockStore(ctrl)
		papi := mock_plugin_api.NewMockPluginAPI(ctrl)
		m := &mscalendar{Env: Env{
			Config:       &config.Config{},
			Dependencies: &Dependencies{Store: s, PluginAPI: papi},
		}}
		return m, s, papi
	}
	newUser := func() *store.User {
		return &store.User{
			MattermostUserID: "user_mm_id",
			Settings:         store.Settings{UpdateStatusFromOptions: store.DNDStatusOption},
			ActiveEvents:     []string{"meeting"},
			LastStatus:       model.StatusOnline,
			BusyStatus:       model.StatusAway,
		}
	}

	t.Run("a status changed manually is kept and the busy period is cleared in one write", func(t *testing.T) {
		m, s, _ := newEngine(t)
		user := newUser()

		// Neither StoreUser (with the user loaded at the start of the sync) nor StoreUserActiveEvents is expected.
		s.EXPECT().ClearUserBusyPeriod("user_mm_id").Return(nil)

		_, changed, err := m.setStatusFromCalendarView(user, &model.Status{Status: model.StatusOnline, Manual: true}, nil, nil)
		require.NoError(t, err)
		assert.False(t, changed)
	})

	t.Run("a failure to clear the busy period is returned", func(t *testing.T) {
		m, s, _ := newEngine(t)
		user := newUser()

		s.EXPECT().ClearUserBusyPeriod("user_mm_id").Return(errors.New("store failed"))

		_, changed, err := m.setStatusFromCalendarView(user, &model.Status{Status: model.StatusOnline, Manual: true}, nil, nil)
		require.Error(t, err)
		assert.False(t, changed)
	})

	t.Run("the status set by the plugin is restored", func(t *testing.T) {
		m, s, papi := newEngine(t)
		user := newUser()

		// Neither StoreUser (with the user loaded at the start of the sync) nor StoreUserActiveEvents is expected.
		gomock.InOrder(
			papi.EXPECT().UpdateMattermostUserStatus("user_mm_id", model.StatusOnline).Return(nil, nil),
			s.EXPECT().ClearUserBusyPeriod("user_mm_id").Return(nil),
		)

		_, changed, err := m.setStatusFromCalendarView(user, &model.Status{Status: model.StatusAway}, nil, nil)
		require.NoError(t, err)
		assert.True(t, changed)
		assert.Equal(t, "", user.BusyStatus)
		assert.Equal(t, "", user.LastStatus)
	})

	t.Run("the confirmation to restore the status is sent before the busy period is cleared", func(t *testing.T) {
		m, s, _ := newEngine(t)
		poster := mock_bot.NewMockPoster(gomock.NewController(t))
		m.Dependencies.Poster = poster
		user := newUser()
		user.Settings.GetConfirmation = true

		gomock.InOrder(
			poster.EXPECT().DMWithAttachments("user_mm_id", gomock.Any()).Return("", nil),
			s.EXPECT().ClearUserBusyPeriod("user_mm_id").Return(nil),
		)

		_, changed, err := m.setStatusFromCalendarView(user, &model.Status{Status: model.StatusAway}, nil, nil)
		require.NoError(t, err)
		assert.True(t, changed)
	})

	t.Run("a failure to clear the busy period after the restore is returned", func(t *testing.T) {
		m, s, papi := newEngine(t)
		user := newUser()

		papi.EXPECT().UpdateMattermostUserStatus("user_mm_id", model.StatusOnline).Return(nil, nil)
		s.EXPECT().ClearUserBusyPeriod("user_mm_id").Return(errors.New("store failed"))

		_, changed, err := m.setStatusFromCalendarView(user, &model.Status{Status: model.StatusAway}, nil, nil)
		require.Error(t, err)
		assert.False(t, changed)
	})

	t.Run("a failed restore keeps the stored user and the active events for the next sync", func(t *testing.T) {
		m, _, papi := newEngine(t)
		user := newUser()
		before := *user

		// Neither StoreUser nor StoreUserActiveEvents is expected.
		papi.EXPECT().UpdateMattermostUserStatus("user_mm_id", model.StatusOnline).Return(nil, errors.New("update failed"))

		_, changed, err := m.setStatusFromCalendarView(user, &model.Status{Status: model.StatusAway}, nil, nil)
		require.Error(t, err)
		assert.False(t, changed)
		assert.Equal(t, before, *user)
	})
}

func TestStatusRuleWithoutStatus(t *testing.T) {
	rules, err := config.ParseStatusRules(`[
		{"title_contains": "Review", "emoji": "eyes"},
		{"title_contains": "Обед", "status": "none", "emoji": "hamburger"}
	]`)
	require.NoError(t, err)
	dndUser := &store.User{Settings: store.Settings{UpdateStatusFromOptions: store.DNDStatusOption}}
	awayUser := &store.User{Settings: store.Settings{UpdateStatusFromOptions: store.AwayStatusOption}}

	organized := testEvent("Code review", remote.EventTypeDefault, "busy", 3)
	organized.IsOrganizer = true
	solo := testEvent("Review notes", remote.EventTypeDefault, "free", 0)
	lunch := testEvent("Обед", remote.EventTypeDefault, "busy", 0)

	events := filterBusyAndAttendeeEvents([]*remote.Event{organized, solo, lunch}, rules)
	assert.Equal(t, []string{"Code review", "Review notes", "Обед"}, eventSubjects(events))
	assert.Equal(t, []string{"Code review", "Review notes"}, eventSubjects(filterStatusChangingEvents(events, rules)),
		"a rule without a status still counts as busy, only none leaves the status unchanged")

	rule := statusRuleOf(rules, organized)
	require.NotNil(t, rule)
	assert.Equal(t, model.StatusDnd, busyStatusForEvent(dndUser, rule), "the user's own setting is used")
	assert.Equal(t, model.StatusAway, busyStatusForEvent(awayUser, rule), "the user's own setting is used")

	emoji, text := customStatusForEvent(organized, rule)
	assert.Equal(t, "eyes", emoji)
	assert.Equal(t, "Code review", text)
}

func TestSetStatusFromCalendarViewAlreadyBusyStoreError(t *testing.T) {
	ctrl := gomock.NewController(t)
	s := mock_store.NewMockStore(ctrl)
	m := &mscalendar{Env: Env{
		Config:       &config.Config{},
		Dependencies: &Dependencies{Store: s},
	}}
	user := &store.User{MattermostUserID: "user_mm_id", Settings: store.Settings{UpdateStatusFromOptions: store.DNDStatusOption}}
	meeting := testEvent("Meeting", remote.EventTypeDefault, "busy", 2)

	// The active events are not stored when the user could not be stored.
	s.EXPECT().StoreUser(user).Return(errors.New("store failed"))

	_, changed, err := m.setStatusFromCalendarView(user, &model.Status{Status: model.StatusDnd, Manual: true}, []*remote.Event{meeting}, nil)
	require.Error(t, err)
	assert.False(t, changed)
}

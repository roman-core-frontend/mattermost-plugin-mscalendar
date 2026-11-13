// Copyright (c) 2019-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	StatusRuleStatusDnd        = "dnd"
	StatusRuleStatusAway       = "away"
	StatusRuleStatusNoChange   = "none"
	StatusRuleTitlePlaceholder = "{title}"
)

// StatusRule maps a calendar event to the Mattermost status that should be set while it is in progress.
// Exactly one condition (EventType or TitleContains) must be set.
type StatusRule struct {
	// EventType matches the remote event type, e.g. "outOfOffice" or "focusTime".
	EventType string `json:"event_type,omitempty"`
	// TitleContains matches events whose title contains this text (case-insensitive).
	TitleContains string `json:"title_contains,omitempty"`

	// Status is "dnd", "away", or "none"/empty to leave the colored status unchanged.
	Status string `json:"status,omitempty"`
	// Emoji is the emoji name for the custom status, e.g. "palm_tree". Empty keeps the default.
	Emoji string `json:"emoji,omitempty"`
	// Text is the custom status text. "{title}" is replaced with the event title. Empty means the event title.
	Text string `json:"text,omitempty"`

	titleContainsLower string
}

// ParseStatusRules parses the JSON array of status rules configured in the System Console.
// An empty string yields no rules. Invalid rules are skipped and reported in the returned error,
// while the valid ones are still returned. A malformed JSON array yields no rules.
func ParseStatusRules(raw string) ([]StatusRule, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}

	var parsed []StatusRule
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("status rules must be a JSON array: %w", err)
	}

	rules := make([]StatusRule, 0, len(parsed))
	var errs []error
	for i, r := range parsed {
		if err := r.normalize(); err != nil {
			errs = append(errs, fmt.Errorf("status rule #%d skipped: %w", i+1, err))
			continue
		}
		rules = append(rules, r)
	}

	return rules, errors.Join(errs...)
}

func (r *StatusRule) normalize() error {
	r.EventType = strings.TrimSpace(r.EventType)
	r.TitleContains = strings.TrimSpace(r.TitleContains)
	r.Status = strings.ToLower(strings.TrimSpace(r.Status))
	r.Emoji = strings.Trim(strings.TrimSpace(r.Emoji), ":")
	r.titleContainsLower = strings.ToLower(r.TitleContains)

	if (r.EventType == "") == (r.TitleContains == "") {
		return errors.New(`exactly one of "event_type" or "title_contains" must be set`)
	}

	switch r.Status {
	case "", StatusRuleStatusNoChange:
		r.Status = StatusRuleStatusNoChange
	case StatusRuleStatusDnd, StatusRuleStatusAway:
	default:
		return fmt.Errorf(`unknown status %q, expected "dnd", "away" or "none"`, r.Status)
	}
	return nil
}

// matches reports whether the rule applies to an event with the given type and lowercased title.
func (r *StatusRule) matches(eventType, titleLower string) bool {
	if r.EventType != "" {
		return strings.EqualFold(r.EventType, eventType)
	}
	if r.TitleContains == "" {
		return false
	}
	needle := r.titleContainsLower
	if needle == "" {
		needle = strings.ToLower(r.TitleContains)
	}
	return strings.Contains(titleLower, needle)
}

// ChangesStatus reports whether the rule sets a colored status.
func (r *StatusRule) ChangesStatus() bool {
	return r.Status == StatusRuleStatusDnd || r.Status == StatusRuleStatusAway
}

// CustomStatusText returns the custom status text for an event with the given title.
func (r *StatusRule) CustomStatusText(title string) string {
	if r.Text == "" {
		return title
	}
	return strings.ReplaceAll(r.Text, StatusRuleTitlePlaceholder, title)
}

// FindStatusRule returns the first rule matching the event, or nil.
// The rules must come from ParseStatusRules.
func FindStatusRule(rules []StatusRule, eventType, title string) *StatusRule {
	if len(rules) == 0 {
		return nil
	}
	titleLower := strings.ToLower(title)
	for i := range rules {
		if rules[i].matches(eventType, titleLower) {
			return &rules[i]
		}
	}
	return nil
}

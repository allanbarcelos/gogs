package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWebhook_HasStatusEvent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		hook Webhook
		want bool
	}{
		{
			name: "send everything",
			hook: Webhook{HookEvent: &HookEvent{SendEverything: true}},
			want: true,
		},
		{
			name: "choose events with status on",
			hook: Webhook{HookEvent: &HookEvent{ChooseEvents: true, HookEvents: HookEvents{Status: true}}},
			want: true,
		},
		{
			name: "choose events with status off",
			hook: Webhook{HookEvent: &HookEvent{ChooseEvents: true, HookEvents: HookEvents{Status: false}}},
			want: false,
		},
		{
			name: "push only",
			hook: Webhook{HookEvent: &HookEvent{PushOnly: true}},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.hook.HasStatusEvent())
		})
	}
}

func TestWebhook_EventsArray_IncludesStatus(t *testing.T) {
	t.Parallel()

	hook := Webhook{HookEvent: &HookEvent{ChooseEvents: true, HookEvents: HookEvents{Status: true}}}
	assert.Contains(t, hook.EventsArray(), string(HookEventTypeStatus))

	hook = Webhook{HookEvent: &HookEvent{ChooseEvents: true, HookEvents: HookEvents{Push: true}}}
	assert.NotContains(t, hook.EventsArray(), string(HookEventTypeStatus))
}

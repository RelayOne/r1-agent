package subscriptions

import (
	"encoding/json"
	"testing"
	"time"
)

// TestUsageResponseSchema validates that the UsageData struct can round-trip
// through JSON encoding/decoding with all expected fields. This is a contract
// test: if Anthropic changes the OAuth usage endpoint schema, this test should
// fail and alert us to update the struct.
func TestUsageResponseSchema(t *testing.T) {
	// Known-good response fixture matching the documented schema
	fixture := `{
		"five_hour": {
			"utilization": 42.5,
			"resets_at": "2026-04-08T15:30:00Z"
		},
		"seven_day": {
			"utilization": 12.3,
			"resets_at": "2026-04-14T00:00:00Z"
		},
		"seven_day_opus": {
			"utilization": 5.0,
			"resets_at": "2026-04-14T00:00:00Z"
		}
	}`

	var data UsageData
	if err := json.Unmarshal([]byte(fixture), &data); err != nil {
		t.Fatalf("Failed to unmarshal fixture: %v", err)
	}

	// Validate five_hour window
	if data.FiveHour.Utilization != 42.5 {
		t.Errorf("FiveHour.Utilization = %v, want 42.5", data.FiveHour.Utilization)
	}
	if data.FiveHour.ResetsAt == nil {
		t.Fatal("FiveHour.ResetsAt is nil, want non-nil")
	}
	expectedReset := time.Date(2026, 4, 8, 15, 30, 0, 0, time.UTC)
	if !data.FiveHour.ResetsAt.Equal(expectedReset) {
		t.Errorf("FiveHour.ResetsAt = %v, want %v", data.FiveHour.ResetsAt, expectedReset)
	}

	// Validate seven_day window
	if data.SevenDay.Utilization != 12.3 {
		t.Errorf("SevenDay.Utilization = %v, want 12.3", data.SevenDay.Utilization)
	}
	if data.SevenDay.ResetsAt == nil {
		t.Fatal("SevenDay.ResetsAt is nil, want non-nil")
	}

	// Validate seven_day_opus window
	if data.SevenDayOpus.Utilization != 5.0 {
		t.Errorf("SevenDayOpus.Utilization = %v, want 5.0", data.SevenDayOpus.Utilization)
	}

	// Round-trip: marshal and unmarshal again
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var roundTrip UsageData
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("Unmarshal round-trip failed: %v", err)
	}

	if roundTrip.FiveHour.Utilization != data.FiveHour.Utilization {
		t.Errorf("Round-trip FiveHour.Utilization mismatch: %v vs %v",
			roundTrip.FiveHour.Utilization, data.FiveHour.Utilization)
	}
	if roundTrip.SevenDay.Utilization != data.SevenDay.Utilization {
		t.Errorf("Round-trip SevenDay.Utilization mismatch: %v vs %v",
			roundTrip.SevenDay.Utilization, data.SevenDay.Utilization)
	}
}

// TestUsageResponseSchema_EmptyResets verifies we handle null resets_at fields
// gracefully (the endpoint returns null when a window hasn't been used).
func TestUsageResponseSchema_EmptyResets(t *testing.T) {
	fixture := `{
		"five_hour": {"utilization": 0, "resets_at": null},
		"seven_day": {"utilization": 0, "resets_at": null},
		"seven_day_opus": {"utilization": 0, "resets_at": null}
	}`

	var data UsageData
	if err := json.Unmarshal([]byte(fixture), &data); err != nil {
		t.Fatalf("Failed to unmarshal fixture with null resets: %v", err)
	}

	if data.FiveHour.ResetsAt != nil {
		t.Errorf("FiveHour.ResetsAt = %v, want nil", data.FiveHour.ResetsAt)
	}
	if data.FiveHour.Utilization != 0 {
		t.Errorf("FiveHour.Utilization = %v, want 0", data.FiveHour.Utilization)
	}
}

// TestUsageResponseSchema_UnknownFields verifies forward compatibility: if
// Anthropic adds new fields, our struct should still parse the known fields.
func TestUsageResponseSchema_UnknownFields(t *testing.T) {
	fixture := `{
		"five_hour": {"utilization": 10, "resets_at": null, "new_field": "surprise"},
		"seven_day": {"utilization": 20, "resets_at": null},
		"seven_day_opus": {"utilization": 0, "resets_at": null},
		"totally_new_window": {"utilization": 99}
	}`

	var data UsageData
	if err := json.Unmarshal([]byte(fixture), &data); err != nil {
		t.Fatalf("Failed to unmarshal fixture with unknown fields: %v", err)
	}

	if data.FiveHour.Utilization != 10 {
		t.Errorf("FiveHour.Utilization = %v, want 10", data.FiveHour.Utilization)
	}
	if data.SevenDay.Utilization != 20 {
		t.Errorf("SevenDay.Utilization = %v, want 20", data.SevenDay.Utilization)
	}
}

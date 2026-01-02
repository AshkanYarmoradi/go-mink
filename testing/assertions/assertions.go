// Package assertions provides event assertion utilities for testing event-sourced systems.
// It includes helpers for comparing events, checking event types, and generating event diffs.
package assertions

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// TB is an alias for testing.TB interface to allow mocking in tests
type TB = testing.TB

// AssertEventTypes checks that the events have the expected types in order.
func AssertEventTypes(t TB, events []interface{}, types ...string) {
	t.Helper()

	if len(events) != len(types) {
		t.Fatalf("Expected %d events, got %d", len(types), len(events))
	}

	for i, expectedType := range types {
		actualType := getTypeName(events[i])
		if actualType != expectedType {
			t.Errorf("Event %d: expected type %s, got %s", i, expectedType, actualType)
		}
	}
}

// AssertEventData checks that a specific event matches the expected data.
func AssertEventData[T any](t TB, event interface{}, expected T) {
	t.Helper()

	actual, ok := event.(T)
	if !ok {
		t.Fatalf("Event is not of expected type %T, got %T", expected, event)
	}

	if !reflect.DeepEqual(actual, expected) {
		t.Errorf("Event data mismatch:\nExpected: %+v\nActual: %+v", expected, actual)
	}
}

// AssertEventCount checks the number of events.
func AssertEventCount(t TB, events []interface{}, expected int) {
	t.Helper()

	if len(events) != expected {
		t.Errorf("Expected %d events, got %d", expected, len(events))
	}
}

// AssertNoEvents checks that no events were produced.
func AssertNoEvents(t TB, events []interface{}) {
	t.Helper()

	if len(events) > 0 {
		t.Errorf("Expected no events, got %d: %+v", len(events), events)
	}
}

// AssertFirstEvent checks the first event matches the expected data.
func AssertFirstEvent[T any](t TB, events []interface{}, expected T) {
	t.Helper()

	if len(events) == 0 {
		t.Fatal("Expected at least one event, got none")
	}

	AssertEventData(t, events[0], expected)
}

// AssertLastEvent checks the last event matches the expected data.
func AssertLastEvent[T any](t TB, events []interface{}, expected T) {
	t.Helper()

	if len(events) == 0 {
		t.Fatal("Expected at least one event, got none")
	}

	AssertEventData(t, events[len(events)-1], expected)
}

// AssertEventAtIndex checks the event at a specific index matches the expected data.
func AssertEventAtIndex[T any](t TB, events []interface{}, index int, expected T) {
	t.Helper()

	if index < 0 || index >= len(events) {
		t.Fatalf("Index %d out of bounds, have %d events", index, len(events))
	}

	AssertEventData(t, events[index], expected)
}

// AssertContainsEvent checks that the events contain an event of the expected type and data.
func AssertContainsEvent[T any](t TB, events []interface{}, expected T) {
	t.Helper()

	for _, event := range events {
		if actual, ok := event.(T); ok {
			if reflect.DeepEqual(actual, expected) {
				return
			}
		}
	}

	t.Errorf("Events do not contain expected event: %+v", expected)
}

// AssertContainsEventType checks that the events contain at least one event of the specified type.
func AssertContainsEventType(t TB, events []interface{}, typeName string) {
	t.Helper()

	for _, event := range events {
		if getTypeName(event) == typeName {
			return
		}
	}

	t.Errorf("Events do not contain event of type %s", typeName)
}

// EventDiff represents a difference between expected and actual events.
type EventDiff struct {
	Index    int
	Expected interface{}
	Actual   interface{}
	Type     DiffType
}

// DiffType represents the type of difference.
type DiffType int

const (
	// DiffMissing indicates an expected event was not present.
	DiffMissing DiffType = iota
	// DiffExtra indicates an unexpected event was present.
	DiffExtra
	// DiffMismatch indicates event data did not match.
	DiffMismatch
)

// String returns a human-readable representation of the diff type.
func (d DiffType) String() string {
	switch d {
	case DiffMissing:
		return "missing"
	case DiffExtra:
		return "extra"
	case DiffMismatch:
		return "mismatch"
	default:
		return "unknown"
	}
}

// DiffEvents compares two event slices and returns the differences.
func DiffEvents(expected, actual []interface{}) []EventDiff {
	var diffs []EventDiff

	maxLen := len(expected)
	if len(actual) > maxLen {
		maxLen = len(actual)
	}

	for i := 0; i < maxLen; i++ {
		var exp, act interface{}
		if i < len(expected) {
			exp = expected[i]
		}
		if i < len(actual) {
			act = actual[i]
		}

		if exp == nil && act != nil {
			diffs = append(diffs, EventDiff{
				Index:  i,
				Actual: act,
				Type:   DiffExtra,
			})
		} else if exp != nil && act == nil {
			diffs = append(diffs, EventDiff{
				Index:    i,
				Expected: exp,
				Type:     DiffMissing,
			})
		} else if !reflect.DeepEqual(exp, act) {
			diffs = append(diffs, EventDiff{
				Index:    i,
				Expected: exp,
				Actual:   act,
				Type:     DiffMismatch,
			})
		}
	}

	return diffs
}

// FormatDiffs formats event diffs as a human-readable string.
func FormatDiffs(diffs []EventDiff) string {
	if len(diffs) == 0 {
		return "no differences"
	}

	var buf strings.Builder
	buf.WriteString("Event differences:\n")

	for _, diff := range diffs {
		buf.WriteString(formatDiff(diff))
	}

	return buf.String()
}

func formatDiff(diff EventDiff) string {
	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("  Event %d (%s):\n", diff.Index, diff.Type))

	switch diff.Type {
	case DiffExtra:
		buf.WriteString(fmt.Sprintf("    + %T %+v (unexpected)\n", diff.Actual, diff.Actual))
	case DiffMissing:
		buf.WriteString(fmt.Sprintf("    - %T %+v (missing)\n", diff.Expected, diff.Expected))
	case DiffMismatch:
		buf.WriteString(fmt.Sprintf("    - %T %+v\n", diff.Expected, diff.Expected))
		buf.WriteString(fmt.Sprintf("    + %T %+v\n", diff.Actual, diff.Actual))
	}

	return buf.String()
}

// AssertEventsEqual compares two event slices and fails if they differ.
func AssertEventsEqual(t TB, expected, actual []interface{}) {
	t.Helper()

	diffs := DiffEvents(expected, actual)
	if len(diffs) > 0 {
		t.Error(FormatDiffs(diffs))
	}
}

// AssertEventsMatch checks that actual events match expected events,
// allowing for extra events at the end.
func AssertEventsMatch(t TB, expected, actual []interface{}) {
	t.Helper()

	if len(actual) < len(expected) {
		t.Fatalf("Expected at least %d events, got %d", len(expected), len(actual))
	}

	for i, exp := range expected {
		if !reflect.DeepEqual(exp, actual[i]) {
			t.Errorf("Event %d mismatch:\nExpected: %+v\nActual: %+v", i, exp, actual[i])
		}
	}
}

// getTypeName returns the type name of a value.
func getTypeName(v interface{}) string {
	if v == nil {
		return "<nil>"
	}
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Name()
}

// EventMatcher is a function that checks if an event matches certain criteria.
type EventMatcher func(event interface{}) bool

// MatchEventType returns a matcher that checks for a specific event type.
func MatchEventType(typeName string) EventMatcher {
	return func(event interface{}) bool {
		return getTypeName(event) == typeName
	}
}

// MatchEvent returns a matcher that checks for exact event equality.
func MatchEvent[T any](expected T) EventMatcher {
	return func(event interface{}) bool {
		actual, ok := event.(T)
		if !ok {
			return false
		}
		return reflect.DeepEqual(actual, expected)
	}
}

// AssertAnyMatch checks that at least one event matches the matcher.
func AssertAnyMatch(t TB, events []interface{}, matcher EventMatcher) {
	t.Helper()

	for _, event := range events {
		if matcher(event) {
			return
		}
	}

	t.Error("No event matched the criteria")
}

// AssertAllMatch checks that all events match the matcher.
func AssertAllMatch(t TB, events []interface{}, matcher EventMatcher) {
	t.Helper()

	for i, event := range events {
		if !matcher(event) {
			t.Errorf("Event %d did not match: %+v", i, event)
		}
	}
}

// AssertNoneMatch checks that no events match the matcher.
func AssertNoneMatch(t TB, events []interface{}, matcher EventMatcher) {
	t.Helper()

	for i, event := range events {
		if matcher(event) {
			t.Errorf("Event %d unexpectedly matched: %+v", i, event)
		}
	}
}

// CountMatches returns the number of events that match the matcher.
func CountMatches(events []interface{}, matcher EventMatcher) int {
	count := 0
	for _, event := range events {
		if matcher(event) {
			count++
		}
	}
	return count
}

// FilterEvents returns events that match the matcher.
func FilterEvents(events []interface{}, matcher EventMatcher) []interface{} {
	var result []interface{}
	for _, event := range events {
		if matcher(event) {
			result = append(result, event)
		}
	}
	return result
}

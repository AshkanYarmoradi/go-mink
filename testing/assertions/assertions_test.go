package assertions

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Test Events
// =============================================================================

type TestOrderCreated struct {
	OrderID    string
	CustomerID string
}

type TestItemAdded struct {
	OrderID  string
	SKU      string
	Quantity int
	Price    float64
}

type TestOrderShipped struct {
	OrderID string
}

// =============================================================================
// Mock Testing Helper
// =============================================================================

// mockT is a mock testing.TB that captures test failures for testing assertion functions
type mockT struct {
	testing.TB
	failed  bool
	message string
	fatal   bool
}

func newMockT() *mockT {
	return &mockT{}
}

func (m *mockT) Helper() {}

func (m *mockT) Errorf(format string, args ...interface{}) {
	m.failed = true
	m.message = format
}

func (m *mockT) Fatalf(format string, args ...interface{}) {
	m.failed = true
	m.fatal = true
	m.message = format
	runtime.Goexit()
}

func (m *mockT) Fatal(args ...interface{}) {
	m.failed = true
	m.fatal = true
	if len(args) > 0 {
		if msg, ok := args[0].(string); ok {
			m.message = msg
		}
	}
	runtime.Goexit()
}

func (m *mockT) Error(args ...interface{}) {
	m.failed = true
	if len(args) > 0 {
		if msg, ok := args[0].(string); ok {
			m.message = msg
		}
	}
}

// runWithMockT runs a function with a mockT and returns whether it failed
func runWithMockT(fn func(*mockT)) (mt *mockT) {
	mt = newMockT()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(mt)
	}()
	<-done
	return mt
}

// =============================================================================
// AssertEventTypes Tests
// =============================================================================

func TestAssertEventTypes(t *testing.T) {
	t.Run("passes when types match", func(t *testing.T) {
		events := []interface{}{
			TestOrderCreated{OrderID: "123", CustomerID: "cust"},
			TestItemAdded{OrderID: "123", SKU: "SKU-1"},
			TestOrderShipped{OrderID: "123"},
		}

		AssertEventTypes(t, events, "TestOrderCreated", "TestItemAdded", "TestOrderShipped")
	})

	t.Run("handles empty events", func(t *testing.T) {
		AssertEventTypes(t, []interface{}{})
	})

	t.Run("handles single event", func(t *testing.T) {
		events := []interface{}{TestOrderCreated{OrderID: "123"}}
		AssertEventTypes(t, events, "TestOrderCreated")
	})

	t.Run("fails when event count mismatch", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			events := []interface{}{TestOrderCreated{OrderID: "123"}}
			AssertEventTypes(m, events, "TestOrderCreated", "TestItemAdded")
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})

	t.Run("fails when types do not match", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			events := []interface{}{TestOrderCreated{OrderID: "123"}}
			AssertEventTypes(m, events, "TestItemAdded")
		})
		assert.True(t, mt.failed)
	})
}

// =============================================================================
// AssertEventData Tests
// =============================================================================

func TestAssertEventData(t *testing.T) {
	t.Run("passes when data matches", func(t *testing.T) {
		event := TestOrderCreated{OrderID: "123", CustomerID: "cust-456"}
		AssertEventData(t, event, TestOrderCreated{OrderID: "123", CustomerID: "cust-456"})
	})

	t.Run("handles complex struct", func(t *testing.T) {
		event := TestItemAdded{OrderID: "123", SKU: "SKU-1", Quantity: 2, Price: 29.99}
		AssertEventData(t, event, TestItemAdded{OrderID: "123", SKU: "SKU-1", Quantity: 2, Price: 29.99})
	})

	t.Run("fails when types do not match", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			event := TestOrderCreated{OrderID: "123"}
			AssertEventData(m, event, TestItemAdded{OrderID: "123"})
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})

	t.Run("fails when data differs", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			event := TestOrderCreated{OrderID: "123", CustomerID: "cust-1"}
			AssertEventData(m, event, TestOrderCreated{OrderID: "123", CustomerID: "cust-2"})
		})
		assert.True(t, mt.failed)
	})
}

// =============================================================================
// AssertEventCount Tests
// =============================================================================

func TestAssertEventCount(t *testing.T) {
	t.Run("passes when count matches", func(t *testing.T) {
		events := []interface{}{
			TestOrderCreated{},
			TestItemAdded{},
		}
		AssertEventCount(t, events, 2)
	})

	t.Run("handles zero events", func(t *testing.T) {
		AssertEventCount(t, []interface{}{}, 0)
	})

	t.Run("fails when count does not match", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			events := []interface{}{TestOrderCreated{}}
			AssertEventCount(m, events, 2)
		})
		assert.True(t, mt.failed)
	})
}

// =============================================================================
// AssertNoEvents Tests
// =============================================================================

func TestAssertNoEvents(t *testing.T) {
	t.Run("passes when no events", func(t *testing.T) {
		AssertNoEvents(t, []interface{}{})
	})

	t.Run("fails when events exist", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			events := []interface{}{TestOrderCreated{OrderID: "123"}}
			AssertNoEvents(m, events)
		})
		assert.True(t, mt.failed)
	})
}

// =============================================================================
// AssertFirstEvent Tests
// =============================================================================

func TestAssertFirstEvent(t *testing.T) {
	t.Run("passes when first event matches", func(t *testing.T) {
		events := []interface{}{
			TestOrderCreated{OrderID: "123", CustomerID: "cust"},
			TestItemAdded{OrderID: "123"},
		}
		AssertFirstEvent(t, events, TestOrderCreated{OrderID: "123", CustomerID: "cust"})
	})

	t.Run("fails when no events", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			AssertFirstEvent(m, []interface{}{}, TestOrderCreated{})
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})

	t.Run("fails when first event does not match", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			events := []interface{}{TestOrderCreated{OrderID: "123"}}
			AssertFirstEvent(m, events, TestOrderCreated{OrderID: "456"})
		})
		assert.True(t, mt.failed)
	})
}

// =============================================================================
// AssertLastEvent Tests
// =============================================================================

func TestAssertLastEvent(t *testing.T) {
	t.Run("passes when last event matches", func(t *testing.T) {
		events := []interface{}{
			TestOrderCreated{OrderID: "123"},
			TestItemAdded{OrderID: "123", SKU: "SKU-1"},
		}
		AssertLastEvent(t, events, TestItemAdded{OrderID: "123", SKU: "SKU-1"})
	})

	t.Run("works with single event", func(t *testing.T) {
		events := []interface{}{
			TestOrderCreated{OrderID: "123"},
		}
		AssertLastEvent(t, events, TestOrderCreated{OrderID: "123"})
	})

	t.Run("fails when no events", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			AssertLastEvent(m, []interface{}{}, TestOrderCreated{})
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})

	t.Run("fails when last event does not match", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			events := []interface{}{TestOrderCreated{OrderID: "123"}}
			AssertLastEvent(m, events, TestOrderCreated{OrderID: "456"})
		})
		assert.True(t, mt.failed)
	})
}

// =============================================================================
// AssertEventAtIndex Tests
// =============================================================================

func TestAssertEventAtIndex(t *testing.T) {
	t.Run("passes when event at index matches", func(t *testing.T) {
		events := []interface{}{
			TestOrderCreated{OrderID: "123"},
			TestItemAdded{OrderID: "123", SKU: "SKU-1"},
			TestOrderShipped{OrderID: "123"},
		}
		AssertEventAtIndex(t, events, 1, TestItemAdded{OrderID: "123", SKU: "SKU-1"})
	})

	t.Run("fails when index out of bounds negative", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			events := []interface{}{TestOrderCreated{OrderID: "123"}}
			AssertEventAtIndex(m, events, -1, TestOrderCreated{})
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})

	t.Run("fails when index out of bounds positive", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			events := []interface{}{TestOrderCreated{OrderID: "123"}}
			AssertEventAtIndex(m, events, 5, TestOrderCreated{})
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})

	t.Run("fails when event at index does not match", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			events := []interface{}{TestOrderCreated{OrderID: "123"}}
			AssertEventAtIndex(m, events, 0, TestOrderCreated{OrderID: "456"})
		})
		assert.True(t, mt.failed)
	})
}

// =============================================================================
// AssertContainsEvent Tests
// =============================================================================

func TestAssertContainsEvent(t *testing.T) {
	t.Run("passes when event exists", func(t *testing.T) {
		events := []interface{}{
			TestOrderCreated{OrderID: "123"},
			TestItemAdded{OrderID: "123", SKU: "SKU-1", Quantity: 2, Price: 10.0},
			TestOrderShipped{OrderID: "123"},
		}
		AssertContainsEvent(t, events, TestItemAdded{OrderID: "123", SKU: "SKU-1", Quantity: 2, Price: 10.0})
	})

	t.Run("fails when event does not exist", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			events := []interface{}{TestOrderCreated{OrderID: "123"}}
			AssertContainsEvent(m, events, TestItemAdded{OrderID: "456"})
		})
		assert.True(t, mt.failed)
	})

	t.Run("fails when event type matches but data differs", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			events := []interface{}{TestOrderCreated{OrderID: "123", CustomerID: "cust-1"}}
			AssertContainsEvent(m, events, TestOrderCreated{OrderID: "123", CustomerID: "cust-2"})
		})
		assert.True(t, mt.failed)
	})
}

// =============================================================================
// AssertContainsEventType Tests
// =============================================================================

func TestAssertContainsEventType(t *testing.T) {
	t.Run("passes when type exists", func(t *testing.T) {
		events := []interface{}{
			TestOrderCreated{OrderID: "123"},
			TestItemAdded{OrderID: "123"},
		}
		AssertContainsEventType(t, events, "TestOrderCreated")
	})

	t.Run("fails when type does not exist", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			events := []interface{}{TestOrderCreated{OrderID: "123"}}
			AssertContainsEventType(m, events, "TestOrderShipped")
		})
		assert.True(t, mt.failed)
	})

	t.Run("fails with empty events", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			AssertContainsEventType(m, []interface{}{}, "TestOrderCreated")
		})
		assert.True(t, mt.failed)
	})
}

// =============================================================================
// DiffEvents Tests
// =============================================================================

func TestDiffEvents(t *testing.T) {
	t.Run("returns empty for identical events", func(t *testing.T) {
		events := []interface{}{
			TestOrderCreated{OrderID: "123"},
			TestItemAdded{OrderID: "123"},
		}
		diffs := DiffEvents(events, events)
		assert.Empty(t, diffs)
	})

	t.Run("detects missing events", func(t *testing.T) {
		expected := []interface{}{
			TestOrderCreated{OrderID: "123"},
			TestItemAdded{OrderID: "123"},
		}
		actual := []interface{}{
			TestOrderCreated{OrderID: "123"},
		}
		diffs := DiffEvents(expected, actual)
		require.Len(t, diffs, 1)
		assert.Equal(t, DiffMissing, diffs[0].Type)
		assert.Equal(t, 1, diffs[0].Index)
	})

	t.Run("detects extra events", func(t *testing.T) {
		expected := []interface{}{
			TestOrderCreated{OrderID: "123"},
		}
		actual := []interface{}{
			TestOrderCreated{OrderID: "123"},
			TestItemAdded{OrderID: "123"},
		}
		diffs := DiffEvents(expected, actual)
		require.Len(t, diffs, 1)
		assert.Equal(t, DiffExtra, diffs[0].Type)
		assert.Equal(t, 1, diffs[0].Index)
	})

	t.Run("detects mismatched events", func(t *testing.T) {
		expected := []interface{}{
			TestOrderCreated{OrderID: "123", CustomerID: "cust-1"},
		}
		actual := []interface{}{
			TestOrderCreated{OrderID: "123", CustomerID: "cust-2"},
		}
		diffs := DiffEvents(expected, actual)
		require.Len(t, diffs, 1)
		assert.Equal(t, DiffMismatch, diffs[0].Type)
	})

	t.Run("handles multiple differences", func(t *testing.T) {
		expected := []interface{}{
			TestOrderCreated{OrderID: "123"},
			TestItemAdded{OrderID: "123", SKU: "SKU-1"},
		}
		actual := []interface{}{
			TestOrderCreated{OrderID: "456"},            // Different
			TestItemAdded{OrderID: "123", SKU: "SKU-2"}, // Different
		}
		diffs := DiffEvents(expected, actual)
		assert.Len(t, diffs, 2)
	})
}

// =============================================================================
// DiffType Tests
// =============================================================================

func TestDiffType_String(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		assert.Equal(t, "missing", DiffMissing.String())
	})

	t.Run("extra", func(t *testing.T) {
		assert.Equal(t, "extra", DiffExtra.String())
	})

	t.Run("mismatch", func(t *testing.T) {
		assert.Equal(t, "mismatch", DiffMismatch.String())
	})

	t.Run("unknown", func(t *testing.T) {
		assert.Equal(t, "unknown", DiffType(99).String())
	})
}

// =============================================================================
// FormatDiffs Tests
// =============================================================================

func TestFormatDiffs(t *testing.T) {
	t.Run("returns no differences message when empty", func(t *testing.T) {
		result := FormatDiffs([]EventDiff{})
		assert.Equal(t, "no differences", result)
	})

	t.Run("formats missing diff", func(t *testing.T) {
		diffs := []EventDiff{
			{Index: 0, Expected: TestOrderCreated{OrderID: "123"}, Type: DiffMissing},
		}
		result := FormatDiffs(diffs)
		assert.Contains(t, result, "missing")
		assert.Contains(t, result, "TestOrderCreated")
	})

	t.Run("formats extra diff", func(t *testing.T) {
		diffs := []EventDiff{
			{Index: 0, Actual: TestItemAdded{OrderID: "123"}, Type: DiffExtra},
		}
		result := FormatDiffs(diffs)
		assert.Contains(t, result, "extra")
		assert.Contains(t, result, "unexpected")
	})

	t.Run("formats mismatch diff", func(t *testing.T) {
		diffs := []EventDiff{
			{
				Index:    0,
				Expected: TestOrderCreated{OrderID: "123"},
				Actual:   TestOrderCreated{OrderID: "456"},
				Type:     DiffMismatch,
			},
		}
		result := FormatDiffs(diffs)
		assert.Contains(t, result, "mismatch")
	})
}

// =============================================================================
// AssertEventsEqual Tests
// =============================================================================

func TestAssertEventsEqual(t *testing.T) {
	t.Run("passes when events equal", func(t *testing.T) {
		events := []interface{}{
			TestOrderCreated{OrderID: "123"},
			TestItemAdded{OrderID: "123"},
		}
		AssertEventsEqual(t, events, events)
	})

	t.Run("fails when events differ", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			expected := []interface{}{TestOrderCreated{OrderID: "123"}}
			actual := []interface{}{TestOrderCreated{OrderID: "456"}}
			AssertEventsEqual(m, expected, actual)
		})
		assert.True(t, mt.failed)
	})

	t.Run("fails when counts differ", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			expected := []interface{}{TestOrderCreated{OrderID: "123"}, TestItemAdded{}}
			actual := []interface{}{TestOrderCreated{OrderID: "123"}}
			AssertEventsEqual(m, expected, actual)
		})
		assert.True(t, mt.failed)
	})
}

// =============================================================================
// AssertEventsMatch Tests
// =============================================================================

func TestAssertEventsMatch(t *testing.T) {
	t.Run("passes when prefix matches", func(t *testing.T) {
		expected := []interface{}{
			TestOrderCreated{OrderID: "123"},
		}
		actual := []interface{}{
			TestOrderCreated{OrderID: "123"},
			TestItemAdded{OrderID: "123"},
		}
		AssertEventsMatch(t, expected, actual)
	})

	t.Run("fails when actual has fewer events", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			expected := []interface{}{TestOrderCreated{}, TestItemAdded{}}
			actual := []interface{}{TestOrderCreated{}}
			AssertEventsMatch(m, expected, actual)
		})
		assert.True(t, mt.failed)
		assert.True(t, mt.fatal)
	})

	t.Run("fails when event mismatch", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			expected := []interface{}{TestOrderCreated{OrderID: "123"}}
			actual := []interface{}{TestOrderCreated{OrderID: "456"}, TestItemAdded{}}
			AssertEventsMatch(m, expected, actual)
		})
		assert.True(t, mt.failed)
	})
}

// =============================================================================
// EventMatcher Tests
// =============================================================================

func TestMatchEventType(t *testing.T) {
	t.Run("returns true for matching type", func(t *testing.T) {
		matcher := MatchEventType("TestOrderCreated")
		assert.True(t, matcher(TestOrderCreated{OrderID: "123"}))
	})

	t.Run("returns false for non-matching type", func(t *testing.T) {
		matcher := MatchEventType("TestItemAdded")
		assert.False(t, matcher(TestOrderCreated{OrderID: "123"}))
	})
}

func TestMatchEvent(t *testing.T) {
	t.Run("returns true for exact match", func(t *testing.T) {
		matcher := MatchEvent(TestOrderCreated{OrderID: "123", CustomerID: "cust"})
		assert.True(t, matcher(TestOrderCreated{OrderID: "123", CustomerID: "cust"}))
	})

	t.Run("returns false for partial match", func(t *testing.T) {
		matcher := MatchEvent(TestOrderCreated{OrderID: "123", CustomerID: "cust-1"})
		assert.False(t, matcher(TestOrderCreated{OrderID: "123", CustomerID: "cust-2"}))
	})

	t.Run("returns false for wrong type", func(t *testing.T) {
		matcher := MatchEvent(TestOrderCreated{OrderID: "123"})
		assert.False(t, matcher(TestItemAdded{OrderID: "123"}))
	})
}

// =============================================================================
// AssertAnyMatch Tests
// =============================================================================

func TestAssertAnyMatch(t *testing.T) {
	t.Run("passes when at least one matches", func(t *testing.T) {
		events := []interface{}{
			TestOrderCreated{OrderID: "123"},
			TestItemAdded{OrderID: "123"},
		}
		AssertAnyMatch(t, events, MatchEventType("TestItemAdded"))
	})

	t.Run("fails when none match", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			events := []interface{}{TestOrderCreated{OrderID: "123"}}
			AssertAnyMatch(m, events, MatchEventType("TestItemAdded"))
		})
		assert.True(t, mt.failed)
	})

	t.Run("fails with empty events", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			AssertAnyMatch(m, []interface{}{}, MatchEventType("TestOrderCreated"))
		})
		assert.True(t, mt.failed)
	})
}

// =============================================================================
// AssertAllMatch Tests
// =============================================================================

func TestAssertAllMatch(t *testing.T) {
	t.Run("passes when all match", func(t *testing.T) {
		events := []interface{}{
			TestOrderCreated{OrderID: "123"},
			TestOrderCreated{OrderID: "456"},
		}
		AssertAllMatch(t, events, MatchEventType("TestOrderCreated"))
	})

	t.Run("fails when not all match", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			events := []interface{}{
				TestOrderCreated{OrderID: "123"},
				TestItemAdded{OrderID: "456"},
			}
			AssertAllMatch(m, events, MatchEventType("TestOrderCreated"))
		})
		assert.True(t, mt.failed)
	})

	t.Run("passes with empty events", func(t *testing.T) {
		AssertAllMatch(t, []interface{}{}, MatchEventType("TestOrderCreated"))
	})
}

// =============================================================================
// AssertNoneMatch Tests
// =============================================================================

func TestAssertNoneMatch(t *testing.T) {
	t.Run("passes when none match", func(t *testing.T) {
		events := []interface{}{
			TestOrderCreated{OrderID: "123"},
			TestOrderCreated{OrderID: "456"},
		}
		AssertNoneMatch(t, events, MatchEventType("TestItemAdded"))
	})

	t.Run("fails when some match", func(t *testing.T) {
		mt := runWithMockT(func(m *mockT) {
			events := []interface{}{
				TestOrderCreated{OrderID: "123"},
				TestItemAdded{OrderID: "456"},
			}
			AssertNoneMatch(m, events, MatchEventType("TestItemAdded"))
		})
		assert.True(t, mt.failed)
	})

	t.Run("passes with empty events", func(t *testing.T) {
		AssertNoneMatch(t, []interface{}{}, MatchEventType("TestOrderCreated"))
	})
}

// =============================================================================
// CountMatches Tests
// =============================================================================

func TestCountMatches(t *testing.T) {
	t.Run("counts matching events", func(t *testing.T) {
		events := []interface{}{
			TestOrderCreated{OrderID: "123"},
			TestItemAdded{OrderID: "123"},
			TestItemAdded{OrderID: "123"},
			TestOrderShipped{OrderID: "123"},
		}
		count := CountMatches(events, MatchEventType("TestItemAdded"))
		assert.Equal(t, 2, count)
	})

	t.Run("returns zero when none match", func(t *testing.T) {
		events := []interface{}{
			TestOrderCreated{OrderID: "123"},
		}
		count := CountMatches(events, MatchEventType("TestItemAdded"))
		assert.Equal(t, 0, count)
	})
}

// =============================================================================
// FilterEvents Tests
// =============================================================================

func TestFilterEvents(t *testing.T) {
	t.Run("filters matching events", func(t *testing.T) {
		events := []interface{}{
			TestOrderCreated{OrderID: "123"},
			TestItemAdded{OrderID: "123", SKU: "SKU-1"},
			TestItemAdded{OrderID: "123", SKU: "SKU-2"},
			TestOrderShipped{OrderID: "123"},
		}
		filtered := FilterEvents(events, MatchEventType("TestItemAdded"))
		require.Len(t, filtered, 2)
	})

	t.Run("returns empty slice when none match", func(t *testing.T) {
		events := []interface{}{
			TestOrderCreated{OrderID: "123"},
		}
		filtered := FilterEvents(events, MatchEventType("TestItemAdded"))
		assert.Empty(t, filtered)
	})
}

// =============================================================================
// getTypeName Tests
// =============================================================================

func TestGetTypeName(t *testing.T) {
	t.Run("returns type name for struct", func(t *testing.T) {
		name := getTypeName(TestOrderCreated{})
		assert.Equal(t, "TestOrderCreated", name)
	})

	t.Run("returns type name for pointer", func(t *testing.T) {
		name := getTypeName(&TestOrderCreated{})
		assert.Equal(t, "TestOrderCreated", name)
	})

	t.Run("returns <nil> for nil", func(t *testing.T) {
		name := getTypeName(nil)
		assert.Equal(t, "<nil>", name)
	})
}

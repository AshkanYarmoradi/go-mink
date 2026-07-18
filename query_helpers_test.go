package mink

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// These tests exercise the pure read-model query-predicate helpers in
// repository.go (isNilValue/toTime/toFloat/compareOrdered/valuesEqual/
// matchContains), the streamVersionFilter used by the fallback stream
// subscription, and two public functional options. All are deterministic and
// dependency-free, so their behaviour is worth pinning down directly.

func TestIsNilValue(t *testing.T) {
	var nilPtr *int
	var nilMap map[string]int
	var nilSlice []int
	var nilChan chan int
	var nilFunc func()
	var nilIface interface{ Foo() }

	tests := []struct {
		name string
		val  interface{}
		want bool
	}{
		{"untyped nil", nil, true},
		{"nil pointer", nilPtr, true},
		{"nil map", nilMap, true},
		{"nil slice", nilSlice, true},
		{"nil chan", nilChan, true},
		{"nil func", nilFunc, true},
		{"nil interface", nilIface, true},
		{"non-nil pointer", new(int), false},
		{"int value", 42, false},
		{"string value", "x", false},
		{"empty struct", struct{}{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isNilValue(tt.val))
		})
	}
}

func TestToTime(t *testing.T) {
	now := time.Now()

	got, ok := toTime(now)
	assert.True(t, ok)
	assert.Equal(t, now, got)

	_, ok = toTime("2020-01-01")
	assert.False(t, ok)

	_, ok = toTime(12345)
	assert.False(t, ok)
}

func TestToFloat(t *testing.T) {
	tests := []struct {
		name string
		val  interface{}
		want float64
		ok   bool
	}{
		{"int", int(3), 3, true},
		{"int64", int64(-7), -7, true},
		{"uint", uint(4), 4, true},
		{"uint8", uint8(255), 255, true},
		{"float32", float32(2.5), 2.5, true},
		{"float64", float64(1.25), 1.25, true},
		{"string", "nope", 0, false},
		{"bool", true, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := toFloat(tt.val)
			assert.Equal(t, tt.ok, ok)
			if tt.ok {
				assert.InDelta(t, tt.want, got, 1e-9)
			}
		})
	}
}

func TestCompareOrdered(t *testing.T) {
	early := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		a, b    interface{}
		wantCmp int
		wantOK  bool
	}{
		{"time before", early, late, -1, true},
		{"time after", late, early, 1, true},
		{"time equal", early, early, 0, true},
		{"time vs non-time", early, 5, 0, false},
		{"int less", 1, 2, -1, true},
		{"int greater", 9, 2, 1, true},
		{"int equal", 3, 3, 0, true},
		{"num vs non-num", 1, "x", 0, false},
		{"string less", "a", "b", -1, true},
		{"string greater", "z", "b", 1, true},
		{"incomparable structs", struct{ X int }{1}, struct{ X int }{2}, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmp, ok := compareOrdered(tt.a, tt.b)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantCmp, cmp)
			}
		})
	}
}

func TestValuesEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b interface{}
		want bool
	}{
		{"equal ints across types", int(5), int64(5), true},
		{"unequal ints", 5, 6, false},
		{"equal strings", "go", "go", true},
		{"equal slices via DeepEqual", []int{1, 2}, []int{1, 2}, true},
		{"unequal slices via DeepEqual", []int{1, 2}, []int{1, 3}, false},
		{"equal maps via DeepEqual", map[string]int{"a": 1}, map[string]int{"a": 1}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, valuesEqual(tt.a, tt.b))
		})
	}
}

func TestMatchContains(t *testing.T) {
	tests := []struct {
		name    string
		val     interface{}
		target  interface{}
		want    bool
		wantErr bool
	}{
		{"substring present", "hello world", "world", true, false},
		{"substring absent", "hello", "xyz", false, false},
		{"string val non-string target", "hello", 5, false, false},
		{"slice membership present", []int{1, 2, 3}, 2, true, false},
		{"slice membership absent", []int{1, 2, 3}, 9, false, false},
		{"string slice membership", []string{"a", "b"}, "b", true, false},
		{"non-string non-slice val", 42, "x", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := matchContains(tt.val, tt.target)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestStreamVersionFilter_Matches(t *testing.T) {
	f := &streamVersionFilter{streamID: "orders-1", fromVersion: 5}

	assert.True(t, f.Matches(StoredEvent{StreamID: "orders-1", Version: 6}),
		"same stream, version after fromVersion")
	assert.False(t, f.Matches(StoredEvent{StreamID: "orders-1", Version: 5}),
		"same stream, version equal to fromVersion is exclusive")
	assert.False(t, f.Matches(StoredEvent{StreamID: "orders-1", Version: 4}),
		"same stream, version before fromVersion")
	assert.False(t, f.Matches(StoredEvent{StreamID: "orders-2", Version: 6}),
		"different stream")
}

func TestWithSagaSweepInterval(t *testing.T) {
	m := &SagaManager{}
	WithSagaSweepInterval(3 * time.Second)(m)
	assert.Equal(t, 3*time.Second, m.sagaSweepInterval)
}

func TestWithProcessingTimeout(t *testing.T) {
	p := &OutboxProcessor{}
	WithProcessingTimeout(2 * time.Minute)(p)
	assert.Equal(t, 2*time.Minute, p.processingTimeout)

	// Non-positive durations are ignored, leaving the prior value intact.
	p2 := &OutboxProcessor{processingTimeout: 7 * time.Second}
	WithProcessingTimeout(0)(p2)
	assert.Equal(t, 7*time.Second, p2.processingTimeout)
	WithProcessingTimeout(-1)(p2)
	assert.Equal(t, 7*time.Second, p2.processingTimeout)
}

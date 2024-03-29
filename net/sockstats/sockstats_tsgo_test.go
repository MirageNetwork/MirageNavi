// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

//go:build tailscale_go && (darwin || ios || android)

package sockstats

import (
	"testing"
	"time"
)

type testTime struct {
	time.Time
}

func (t *testTime) now() time.Time {
	return t.Time
}

func (t *testTime) Add(d time.Duration) {
	t.Time = t.Time.Add(d)
}

func TestRadioMonitor(t *testing.T) {
	tests := []struct {
		name string
		activity func(*testTime, *radioMonitor)
		want int64
	}{
		{
			"no activity",
			func(_ *testTime, _ *radioMonitor) {},
			0,
		},
		{
			"active, 10 sec idle",
			func(tt *testTime, rm *radioMonitor) {
				rm.active()
				tt.Add(10 * time.Second)
			},
			50, // radio on 5 seconds of every 10 seconds
		},
		{
			"400 iterations: 2 sec active, 1 min idle",
			func(tt *testTime, rm *radioMonitor) {
				// 400 iterations to ensure values loop back around rm.usage array
				for i := 0; i < 400; i++ {
					rm.active()
					tt.Add(1 * time.Second)
					rm.active()
					tt.Add(1 * time.Minute)
				}
			},
			10, // radio on 6 seconds of every minute
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tm := &testTime{time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)}
			rm := &radioMonitor{
				startTime: tm.Time.Unix(),
				now:       tm.now,
			}
			tt.activity(tm, rm)
			got := rm.radioHighPercent()
			if got != tt.want {
				t.Errorf("got radioOnPercent %d, want %d", got, tt.want)
			}
		})
	}
}

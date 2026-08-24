package main

import "testing"

func newRangeTestApp() *App {
	store := &Store{db: &DB{}}
	store.db.Users = []*User{
		{ID: 1, Username: "user1", Ranges: []PortRange{{Start: 10000, End: 11000}}},
	}
	return &App{store: store}
}

func TestValidateUserRanges(t *testing.T) {
	app := newRangeTestApp()

	cases := []struct {
		name    string
		ranges  []PortRange
		exclude int
		valid   bool
	}{
		{name: "available range", ranges: []PortRange{{Start: 11001, End: 12000}}, valid: true},
		{name: "overlaps another user", ranges: []PortRange{{Start: 10500, End: 10600}}, valid: false},
		{name: "invalid upper port", ranges: []PortRange{{Start: 10500, End: 106000}}, valid: false},
		{name: "invalid reversed range", ranges: []PortRange{{Start: 11000, End: 10000}}, valid: false},
		{name: "overlaps own old range excluded", ranges: []PortRange{{Start: 10000, End: 11000}}, exclude: 1, valid: true},
		{name: "overlaps within request", ranges: []PortRange{{Start: 12000, End: 12500}, {Start: 12400, End: 13000}}, valid: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, got := app.validateUserRanges(tc.ranges, tc.exclude)
			if got != tc.valid {
				t.Fatalf("validateUserRanges(%v): got valid=%v, want %v", tc.ranges, got, tc.valid)
			}
		})
	}
}

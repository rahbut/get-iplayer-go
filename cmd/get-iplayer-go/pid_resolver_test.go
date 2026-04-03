package main

import "testing"

func TestResolvePID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		hasError bool
	}{
		{"m0012345", "m0012345", false},
		{"https://www.bbc.co.uk/iplayer/episode/m0012345/show-name", "m0012345", false},
		{"http://bbc.co.uk/programmes/b006q2x0", "b006q2x0", false},
		{"invalid", "", true},
		{"http://google.com/", "", true},
		{"https://www.bbc.co.uk/iplayer/episode/m0012345", "m0012345", false},
	}

	for _, tc := range tests {
		pid, err := ResolvePID(tc.input)
		if tc.hasError {
			if err == nil {
				t.Errorf("Expected error for input %s, got nil", tc.input)
			}
		} else {
			if err != nil {
				t.Errorf("Unexpected error for input %s: %v", tc.input, err)
			}
			if pid != tc.expected {
				t.Errorf("For input %s, expected %s, got %s", tc.input, tc.expected, pid)
			}
		}
	}
}

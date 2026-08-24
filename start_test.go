package main

import (
	"reflect"
	"testing"
)

func TestParseServiceSelection(t *testing.T) {
	names := []string{"billing_service", "collect_service", "cd_api"}
	aliases := []string{"Billing", "Collect", "Cd-api"}

	tests := []struct {
		name    string
		input   string
		want    []int
		wantErr bool
	}{
		{name: "numbers", input: "1,3", want: []int{0, 2}},
		{name: "names", input: "billing_service collect_service", want: []int{0, 1}},
		{name: "aliases", input: "Collect, Cd-api", want: []int{1, 2}},
		{name: "mixed", input: "1, Collect, cd_api", want: []int{0, 1, 2}},
		{name: "dupes preserve first-mention order", input: "2 1 2", want: []int{1, 0}},
		{name: "unknown token", input: "1, zzz", wantErr: true},
		{name: "number out of range", input: "9", wantErr: true},
		{name: "empty input", input: "", want: nil},
		{name: "whitespace only input", input: "  ,  ", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseServiceSelection(tt.input, names, aliases)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseServiceSelection(%q) = %v, nil, want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseServiceSelection(%q) unexpected error: %v", tt.input, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseServiceSelection(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

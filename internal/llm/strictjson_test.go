package llm

import (
	"errors"
	"testing"
)

func TestRejectDuplicateJSONMembersRecursively(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr bool
	}{
		{name: "distinct nested members", data: `{"outer":{"value":1},"items":[{"value":2}]}`},
		{name: "duplicate root member", data: `{"value":1,"value":2}`, wantErr: true},
		{name: "duplicate nested member", data: `{"outer":{"value":1,"value":2}}`, wantErr: true},
		{name: "duplicate member in array object", data: `{"items":[{"value":1,"value":2}]}`, wantErr: true},
		{name: "multiple values", data: `{} {}`, wantErr: true},
		{name: "invalid json", data: `{"value":`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := rejectDuplicateJSONMembers([]byte(test.data))
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestRejectDuplicateJSONMembersRejectsInvalidUTF8(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{name: "invalid key", data: []byte{'{', '"', 0xff, '"', ':', '1', '}'}},
		{name: "invalid value", data: []byte{'{', '"', 'v', 'a', 'l', 'u', 'e', '"', ':', '"', 0xff, '"', '}'}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := rejectDuplicateJSONMembers(test.data); !errors.Is(err, errInvalidJSON) {
				t.Fatalf("error = %v, want errInvalidJSON", err)
			}
		})
	}
}

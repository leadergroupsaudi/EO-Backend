package services

import (
	"strings"
	"testing"

	"gorm.io/datatypes"
)

// Only field types that need no database (user, org_unit and
// linked_ticket_type look rows up).
const testFields = `[
 {"label":"Subject","handle":"subject","type":"text","required":true},
 {"label":"Budget","handle":"budget","type":"number"},
 {"label":"Start","handle":"start","type":"date"},
 {"label":"Urgent","handle":"urgent","type":"checkbox"},
 {"label":"Topic","handle":"topic","type":"select","options":["Economy","Health"]},
 {"label":"Tags","handle":"tags","type":"text","multi":true},
 {"label":"Evidence","handle":"evidence","type":"file","required":true,"multi":true}
]`

func TestValidateAttributes(t *testing.T) {
	cases := []struct {
		name     string
		attrs    map[string]interface{}
		required bool
		wantErr  []string // substrings; empty means valid
	}{
		{"valid", map[string]interface{}{"subject": "x", "budget": 5.0, "start": "2027-03-31", "urgent": true, "topic": "Health", "tags": []interface{}{"a", "b"}}, true, nil},
		{"required missing", map[string]interface{}{}, true, []string{"Subject is required"}},
		{"required skipped for auto-created", map[string]interface{}{}, false, nil},
		{"unknown key", map[string]interface{}{"subject": "x", "colour": "red"}, true, []string{"colour: not a field"}},
		{"wrong types", map[string]interface{}{"subject": 1.0, "budget": "five", "start": "31/03/2027", "urgent": "yes"}, true,
			[]string{"Subject: must be text", "Budget: must be a number", "Start: must be a date", "Urgent: must be true or false"}},
		{"bad option", map[string]interface{}{"subject": "x", "topic": "Sport"}, true, []string{"Topic: must be one of Economy, Health"}},
		{"multi needs list", map[string]interface{}{"subject": "x", "tags": "a"}, true, []string{"Tags: must be a list"}},
		{"single refuses list", map[string]interface{}{"subject": []interface{}{"x"}}, true, []string{"Subject: takes one value"}},
		{"file at create", map[string]interface{}{"subject": "x", "evidence": []interface{}{"f"}}, true, []string{"Evidence: add files as attachments"}},
	}
	for _, c := range cases {
		err := validateAttributes(nil, datatypes.JSON(testFields), c.attrs, c.required)
		if len(c.wantErr) == 0 {
			if err != nil {
				t.Errorf("%s: unexpected error %v", c.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: expected an error", c.name)
			continue
		}
		for _, w := range c.wantErr {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: error %q does not contain %q", c.name, err, w)
			}
		}
	}
}

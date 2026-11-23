package services

// Checks a ticket's attributes against its type's field definitions
// (ticket_types.fields). The field's handle is the key in attributes.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const maxTextLength = 4000

// validateAttributes returns every problem found, joined into one error.
// checkRequired is false for tickets raised automatically by a move
// (create_linked): their fields are filled in later.
func validateAttributes(tx *gorm.DB, fieldsJSON datatypes.JSON, attrs map[string]interface{}, checkRequired bool) error {
	var defs []models.TicketFieldDef
	if len(fieldsJSON) > 0 {
		if err := json.Unmarshal(fieldsJSON, &defs); err != nil {
			return errors.New("the ticket type's fields could not be read")
		}
	}
	known := map[string]models.TicketFieldDef{}
	for _, f := range defs {
		known[f.Handle] = f
	}

	var errs []string
	for k := range attrs {
		if _, ok := known[k]; !ok {
			errs = append(errs, fmt.Sprintf("%s: not a field of this ticket type", k))
		}
	}

	for _, f := range defs {
		v, present := attrs[f.Handle]
		empty := !present || v == nil || v == "" || isEmptyList(v)

		// Files are uploaded after the ticket exists (POST /tickets/:id/attachments).
		if f.Type == "file" {
			if !empty {
				errs = append(errs, f.Label+": add files as attachments after the ticket is created")
			}
			continue
		}
		if empty {
			if f.Required && checkRequired {
				errs = append(errs, f.Label+" is required")
			}
			continue
		}

		values := []interface{}{v}
		if f.Multi {
			list, ok := v.([]interface{})
			if !ok {
				errs = append(errs, f.Label+": must be a list")
				continue
			}
			values = list
		} else if _, isList := v.([]interface{}); isList {
			errs = append(errs, f.Label+": takes one value, not a list")
			continue
		}
		for _, one := range values {
			if msg := checkFieldValue(tx, f, one); msg != "" {
				errs = append(errs, f.Label+": "+msg)
			}
		}
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func isEmptyList(v interface{}) bool {
	list, ok := v.([]interface{})
	return ok && len(list) == 0
}

// checkFieldValue checks one value against the field's type; "" means fine.
func checkFieldValue(tx *gorm.DB, f models.TicketFieldDef, v interface{}) string {
	switch f.Type {
	case "text":
		s, ok := v.(string)
		if !ok {
			return "must be text"
		}
		if len(s) > maxTextLength {
			return fmt.Sprintf("must be at most %d characters", maxTextLength)
		}

	case "number":
		if _, ok := v.(float64); !ok {
			return "must be a number"
		}

	case "date", "date_hijri_gregorian":
		s, ok := v.(string)
		if !ok {
			return "must be a date (YYYY-MM-DD)"
		}
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return "must be a date (YYYY-MM-DD)"
		}

	case "checkbox":
		if _, ok := v.(bool); !ok {
			return "must be true or false"
		}

	case "select":
		s, ok := v.(string)
		if !ok || s == "" {
			return "must be one of the options"
		}
		if len(f.Options) > 0 && !contains(f.Options, s) {
			return "must be one of " + strings.Join(f.Options, ", ")
		}

	case "user":
		return checkExists(tx, "users", v, "a user")

	case "org_unit":
		return checkExists(tx, "departments", v, "an org unit")

	case "linked_ticket_type":
		id, ok := asUUID(v)
		if !ok {
			return "must be a ticket id"
		}
		q := tx.Table("tickets t").Where("t.id = ? AND t.deleted_at IS NULL", id)
		if f.LinkType != "" {
			q = q.Joins("JOIN ticket_types tt ON tt.id = t.ticket_type_id").
				Where("tt.code = ? OR tt.handle = ?", strings.ToUpper(f.LinkType), f.LinkType)
		}
		var n int64
		q.Count(&n)
		if n == 0 {
			if f.LinkType != "" {
				return "no " + f.LinkType + " ticket with id " + id.String()
			}
			return "no ticket with id " + id.String()
		}
	}
	return ""
}

func checkExists(tx *gorm.DB, table string, v interface{}, what string) string {
	id, ok := asUUID(v)
	if !ok {
		return "must be the id of " + what
	}
	var n int64
	tx.Table(table).Where("id = ? AND deleted_at IS NULL AND is_active = ?", id, true).Count(&n)
	if n == 0 {
		return "no active " + strings.TrimPrefix(strings.TrimPrefix(what, "an "), "a ") + " with id " + id.String()
	}
	return ""
}

func asUUID(v interface{}) (uuid.UUID, bool) {
	s, ok := v.(string)
	if !ok {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(s)
	return id, err == nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

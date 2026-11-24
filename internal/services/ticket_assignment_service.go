package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var assignmentFields = []struct{ handle, label string }{
	{"owning_body", "Owning body"},
	{"owner", "Owner"},
	{"priority", "Priority"},
	{"due_date", "Due date"},
	{"expected_back", "Expected back"},
}

func (s *TicketService) Assign(ctx context.Context, id uuid.UUID, req *models.TicketAssignmentRequest, userID uuid.UUID) (*models.TicketDetail, error) {
	// The body carries the values under "attributes", keyed by field handle,
	// as ticket creation does. Only the assignment fields are accepted here.
	values := map[string]string{}
	var bad []string
	for k, v := range req.Attributes {
		known := false
		for _, f := range assignmentFields {
			if f.handle == k {
				known = true
				switch x := v.(type) {
				case nil:
				case string:
					values[k] = strings.TrimSpace(x)
				default:
					bad = append(bad, f.label+" must be text")
				}
			}
		}
		if !known {
			bad = append(bad, k+": not part of the assignment")
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return nil, errors.New(strings.Join(bad, "; "))
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var t models.Ticket
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Preload("TicketType").First(&t, "id = ?", id).Error; err != nil {
			return errors.New("ticket not found")
		}
		if t.ClosedAt != nil {
			return errors.New("a closed ticket cannot be reassigned")
		}

		var defs []models.TicketFieldDef
		if t.TicketType != nil && len(t.TicketType.Fields) > 0 {
			_ = json.Unmarshal(t.TicketType.Fields, &defs)
		}
		labels := map[string]string{}
		for _, f := range defs {
			labels[f.Handle] = f.Label
		}
		label := func(handle, fallback string) string {
			if l, ok := labels[handle]; ok && l != "" {
				return l
			}
			return fallback
		}

		// Every value is required (AC-02.3: name what is missing).
		var missing []string
		for _, f := range assignmentFields {
			if values[f.handle] == "" {
				missing = append(missing, label(f.handle, f.label)+" is required")
			}
		}
		if len(missing) > 0 {
			return errors.New(strings.Join(missing, "; "))
		}
		// The type must carry the fields; due_date may live on the ticket only.
		for _, f := range assignmentFields {
			if _, ok := labels[f.handle]; !ok && f.handle != "due_date" {
				return fmt.Errorf("%s tickets have no %s field", t.TicketType.Name, strings.ToLower(f.label))
			}
		}

		due, err := time.Parse("2006-01-02", values["due_date"])
		if err != nil {
			return fmt.Errorf("%s must be a date (YYYY-MM-DD)", label("due_date", "Due date"))
		}
		if err := checkDueWithinParent(tx, &t, due); err != nil {
			return err
		}

		attrs := map[string]interface{}{}
		if len(t.Attributes) > 0 {
			_ = json.Unmarshal(t.Attributes, &attrs)
		}
		changes := map[string]interface{}{}
		for _, f := range assignmentFields {
			if _, ok := labels[f.handle]; !ok {
				continue
			}
			old := fmt.Sprint(attrs[f.handle])
			if attrs[f.handle] == nil {
				old = ""
			}
			if old != values[f.handle] {
				changes[f.handle] = map[string]string{"from": old, "to": values[f.handle]}
			}
			attrs[f.handle] = values[f.handle]
		}
		// Checks the values themselves: a real department and user, a priority
		// from the list, text length. Other fields are not re-required here.
		if err := validateAttributes(tx, t.TicketType.Fields, attrs, false); err != nil {
			return err
		}

		raw, _ := json.Marshal(attrs)
		if err := tx.Model(&models.Ticket{}).Where("id = ?", t.ID).Updates(map[string]interface{}{
			"attributes": datatypes.JSON(raw),
			"due_date":   due,
			"updated_at": time.Now(),
		}).Error; err != nil {
			return err
		}

		desc := fmt.Sprintf("Assigned to %s · %s, priority %s, due %s",
			departmentName(tx, values["owning_body"]), userDisplayName(tx, values["owner"]),
			values["priority"], due.Format("2 Jan 2006"))
		return logActivity(tx, t.ID, models.TicketActivityAssigned, desc, map[string]interface{}{"changes": changes}, userID)
	})
	if err != nil {
		return nil, err
	}
	return s.GetTicket(ctx, id)
}

// checkDueWithinParent applies the link's "due within parent" rule to a new
// due date, as ticket creation does.
func checkDueWithinParent(tx *gorm.DB, t *models.Ticket, due time.Time) error {
	if t.ParentID == nil {
		return nil
	}
	var p models.Ticket
	if err := tx.Preload("TicketType").First(&p, "id = ?", *t.ParentID).Error; err != nil || p.DueDate == nil {
		return nil
	}
	var link models.TicketTypeLink
	if err := tx.Where("parent_type_id = ? AND child_type_id = ?", p.TicketTypeID, t.TicketTypeID).First(&link).Error; err != nil {
		return nil
	}
	if link.DueWithinParent && due.After(*p.DueDate) {
		return fmt.Errorf("the due date must fall on or before the parent's (%s)", p.DueDate.Format("2 Jan 2006"))
	}
	return nil
}

func departmentName(db *gorm.DB, id string) string {
	var names []string
	db.Table("departments").Where("id = ?", id).Limit(1).Pluck("name", &names)
	if len(names) == 0 || names[0] == "" {
		return id
	}
	return names[0]
}

func userDisplayName(db *gorm.DB, id string) string {
	var names []string
	db.Table("users u").Where("u.id = ?", id).Limit(1).Pluck(userName, &names)
	if len(names) == 0 || names[0] == "" {
		return id
	}
	return names[0]
}

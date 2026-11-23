package services

// Ticket type service (M1): create, edit, set fields, publish and delete ticket
// types through the API, so a new type needs no code change or release.
//
// A new type starts as a draft. Choosing a parent type creates the parent →
// child link (with no behaviours); how the link behaves is then set with
// PUT /ticket-types/:parent/links. A type goes live only after the
// "Before it can go live" checks pass.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

var (
	typeCodeRe    = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}$`)
	typeHandleRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{1,19}$`)
	fieldHandleRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,49}$`)
	patternTokRe  = regexp.MustCompile(`\{[^}]*\}`)
)

// CreateType creates a draft ticket type.
func (s *TicketService) CreateType(ctx context.Context, req *models.TicketTypeCreateRequest) (*models.TicketType, error) {
	var out *models.TicketType
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		t := models.TicketType{
			Code:             strings.ToUpper(strings.TrimSpace(req.Code)),
			Handle:           strings.TrimSpace(req.Handle),
			Name:             strings.TrimSpace(req.Name),
			NameAr:           strings.TrimSpace(req.NameAr),
			Level:            req.Level,
			Subsystem:        strings.ToUpper(strings.TrimSpace(req.Subsystem)),
			ReferencePattern: strings.TrimSpace(req.ReferencePattern),
			Status:           models.TicketTypeDraft,
		}
		if err := validateTypeIdentity(tx, &t, uuid.Nil); err != nil {
			return err
		}
		parent, err := resolveParentType(tx, &t, req.ParentTypeCode)
		if err != nil {
			return err
		}
		if t.Subsystem == "" {
			if parent == nil {
				return errors.New("subsystem is required for a main or universal type")
			}
			t.Subsystem = parent.Subsystem
		}
		if err := validateReferencePattern(t.Level, t.ReferencePattern); err != nil {
			return err
		}
		if req.WorkflowID != nil && *req.WorkflowID != "" {
			id, err := resolveWorkflow(tx, *req.WorkflowID)
			if err != nil {
				return err
			}
			t.WorkflowID = &id
		}
		fields, err := normaliseFields(tx, req.Fields)
		if err != nil {
			return err
		}
		t.Fields = fields
		if req.SortOrder != nil {
			t.SortOrder = *req.SortOrder
		} else {
			var max int
			tx.Model(&models.TicketType{}).Select("COALESCE(MAX(sort_order), -1)").Scan(&max)
			t.SortOrder = max + 1
		}
		if err := tx.Create(&t).Error; err != nil {
			return err
		}
		// Choosing a parent creates the link; its behaviour is set on the parent's Linked types.
		if parent != nil {
			if err := ensureLink(tx, parent, &t); err != nil {
				return err
			}
		}
		out = &t
		return nil
	})
	return out, err
}

// UpdateType changes a ticket type. Code, handle, level and parent are locked
// once the type is live; the workflow is locked once tickets of the type exist.
func (s *TicketService) UpdateType(ctx context.Context, code string, req *models.TicketTypeUpdateRequest) (*models.TicketType, error) {
	var out *models.TicketType
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var t models.TicketType
		if err := tx.Where("code = ?", strings.ToUpper(code)).First(&t).Error; err != nil {
			return fmt.Errorf("ticket type %s not found", code)
		}
		live := t.Status == models.TicketTypeLive
		lockedIfLive := func(name string, changed bool) error {
			if live && changed {
				return fmt.Errorf("%s cannot change once the type is live", name)
			}
			return nil
		}

		oldParentID := t.ParentTypeID
		if req.Code != nil {
			v := strings.ToUpper(strings.TrimSpace(*req.Code))
			if err := lockedIfLive("code", v != t.Code); err != nil {
				return err
			}
			t.Code = v
		}
		if req.Handle != nil {
			v := strings.TrimSpace(*req.Handle)
			if err := lockedIfLive("handle", v != t.Handle); err != nil {
				return err
			}
			t.Handle = v
		}
		if req.Level != nil {
			if err := lockedIfLive("level", *req.Level != t.Level); err != nil {
				return err
			}
			t.Level = *req.Level
		}
		if req.Name != nil {
			t.Name = strings.TrimSpace(*req.Name)
		}
		if req.NameAr != nil {
			t.NameAr = strings.TrimSpace(*req.NameAr)
		}
		if req.Subsystem != nil {
			t.Subsystem = strings.ToUpper(strings.TrimSpace(*req.Subsystem))
		}
		if req.ReferencePattern != nil {
			t.ReferencePattern = strings.TrimSpace(*req.ReferencePattern)
		}
		if req.SortOrder != nil {
			t.SortOrder = *req.SortOrder
		}
		if err := validateTypeIdentity(tx, &t, t.ID); err != nil {
			return err
		}

		// Parent: re-resolve when asked, or when the level changed.
		parentCode := ""
		if req.ParentTypeCode != nil {
			parentCode = *req.ParentTypeCode
		} else if t.ParentTypeID != nil {
			var p models.TicketType
			if tx.First(&p, "id = ?", *t.ParentTypeID).Error == nil {
				parentCode = p.Code
			}
		}
		parent, err := resolveParentType(tx, &t, parentCode)
		if err != nil {
			return err
		}
		if err := lockedIfLive("parent type", !sameUUID(oldParentID, t.ParentTypeID)); err != nil {
			return err
		}
		if err := validateReferencePattern(t.Level, t.ReferencePattern); err != nil {
			return err
		}

		if req.WorkflowID != nil {
			var newID *uuid.UUID
			if *req.WorkflowID != "" {
				id, err := resolveWorkflow(tx, *req.WorkflowID)
				if err != nil {
					return err
				}
				newID = &id
			}
			if !sameUUID(newID, t.WorkflowID) {
				if n := countTicketsOfType(tx, t.ID); n > 0 {
					return fmt.Errorf("the workflow cannot change while %d ticket(s) of this type exist", n)
				}
				t.WorkflowID = newID
			}
		}

		if err := tx.Save(&t).Error; err != nil {
			return err
		}
		// Keep the parent → child link in step with the parent type.
		if !sameUUID(oldParentID, t.ParentTypeID) {
			if oldParentID != nil {
				if err := removeLink(tx, *oldParentID, t.ID); err != nil {
					return err
				}
			}
			if parent != nil {
				if err := ensureLink(tx, parent, &t); err != nil {
					return err
				}
			}
		}
		// The workflow decides where the closure requirements go.
		if err := syncClosureRequirements(tx, &t); err != nil {
			return err
		}
		out = &t
		return nil
	})
	return out, err
}

// SetFields replaces the type's field definitions (the Fields tab).
func (s *TicketService) SetFields(ctx context.Context, code string, fields []models.TicketFieldDef) (*models.TicketType, error) {
	db := s.db.WithContext(ctx)
	var t models.TicketType
	if err := db.Where("code = ?", strings.ToUpper(code)).First(&t).Error; err != nil {
		return nil, fmt.Errorf("ticket type %s not found", code)
	}
	raw, err := normaliseFields(db, fields)
	if err != nil {
		return nil, err
	}
	if t.Status == models.TicketTypeLive {
		if err := checkNoRemovedRequiredFieldsInUse(t.Fields, raw); err != nil {
			return nil, err
		}
	}
	if err := db.Model(&t).Update("fields", raw).Error; err != nil {
		return nil, err
	}
	t.Fields = raw
	return &t, nil
}

// PublishType makes a draft type live once every go-live check passes.
func (s *TicketService) PublishType(ctx context.Context, code string) (*models.TicketType, []models.GoLiveCheck, error) {
	db := s.db.WithContext(ctx)
	var t models.TicketType
	if err := db.Where("code = ?", strings.ToUpper(code)).First(&t).Error; err != nil {
		return nil, nil, fmt.Errorf("ticket type %s not found", code)
	}
	if t.Status == models.TicketTypeLive {
		return nil, nil, fmt.Errorf("%s is already live", t.Name)
	}
	checks := goLiveChecks(db, &t)
	var failed []string
	for _, c := range checks {
		if !c.OK {
			failed = append(failed, c.Label)
		}
	}
	if len(failed) > 0 {
		return nil, checks, fmt.Errorf("%s cannot go live yet: %s", t.Name, strings.Join(failed, "; "))
	}
	if err := db.Model(&t).Update("status", models.TicketTypeLive).Error; err != nil {
		return nil, checks, err
	}
	t.Status = models.TicketTypeLive
	return &t, checks, nil
}

// DeleteType removes a type that is not live, has no tickets and no sub-types.
func (s *TicketService) DeleteType(ctx context.Context, code string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var t models.TicketType
		if err := tx.Where("code = ?", strings.ToUpper(code)).First(&t).Error; err != nil {
			return fmt.Errorf("ticket type %s not found", code)
		}
		if t.Status == models.TicketTypeLive {
			return fmt.Errorf("%s is live and cannot be deleted", t.Name)
		}
		if n := countTicketsOfType(tx, t.ID); n > 0 {
			return fmt.Errorf("%s has %d ticket(s) and cannot be deleted", t.Name, n)
		}
		var subs int64
		tx.Model(&models.TicketType{}).Where("parent_type_id = ?", t.ID).Count(&subs)
		if subs > 0 {
			return fmt.Errorf("%s has %d sub-type(s); delete or move them first", t.Name, subs)
		}
		// Links where this type is the child: remove them and rebuild the
		// parents' closure requirements, so no requirement points at a missing type.
		var asChild []models.TicketTypeLink
		tx.Where("child_type_id = ?", t.ID).Find(&asChild)
		if err := tx.Where("parent_type_id = ? OR child_type_id = ?", t.ID, t.ID).Delete(&models.TicketTypeLink{}).Error; err != nil {
			return err
		}
		for _, l := range asChild {
			var p models.TicketType
			if tx.First(&p, "id = ?", l.ParentTypeID).Error == nil {
				if err := syncClosureRequirements(tx, &p); err != nil {
					return err
				}
			}
		}
		// Hard delete: the code and handle must be free to reuse for a draft that was never live.
		return tx.Unscoped().Delete(&t).Error
	})
}

func validateTypeIdentity(tx *gorm.DB, t *models.TicketType, selfID uuid.UUID) error {
	if !typeCodeRe.MatchString(t.Code) {
		return errors.New("code must be 2–10 capital letters or digits, starting with a letter (e.g. DIR, MED)")
	}
	if !typeHandleRe.MatchString(t.Handle) {
		return errors.New("handle must be 2–20 lowercase letters, digits or _, starting with a letter (e.g. directive)")
	}
	if t.Name == "" {
		return errors.New("name is required")
	}
	switch t.Level {
	case models.TicketLevelMain, models.TicketLevelSub, models.TicketLevelSubSub, models.TicketLevelUniversal:
	default:
		return errors.New("level must be main, sub, sub_sub or universal")
	}
	var n int64
	tx.Unscoped().Model(&models.TicketType{}).Where("code = ? AND id <> ?", t.Code, selfID).Count(&n)
	if n > 0 {
		return fmt.Errorf("code %s is already used", t.Code)
	}
	tx.Unscoped().Model(&models.TicketType{}).Where("handle = ? AND id <> ?", t.Handle, selfID).Count(&n)
	if n > 0 {
		return fmt.Errorf("handle %s is already used", t.Handle)
	}
	return nil
}

// resolveParentType checks the parent against the level and sets t.ParentTypeID.
//
//	main, universal → no parent
//	sub             → parent must be a main type
//	sub_sub         → parent must be a sub type
func resolveParentType(tx *gorm.DB, t *models.TicketType, parentCode string) (*models.TicketType, error) {
	parentCode = strings.ToUpper(strings.TrimSpace(parentCode))
	switch t.Level {
	case models.TicketLevelMain, models.TicketLevelUniversal:
		if parentCode != "" {
			return nil, fmt.Errorf("a %s type has no parent type", t.Level)
		}
		t.ParentTypeID = nil
		return nil, nil
	}
	if parentCode == "" {
		return nil, fmt.Errorf("a %s type needs a parent type", strings.ReplaceAll(t.Level, "_", "-"))
	}
	var p models.TicketType
	if err := tx.Where("code = ?", parentCode).First(&p).Error; err != nil {
		return nil, fmt.Errorf("parent type %s not found", parentCode)
	}
	if p.ID == t.ID {
		return nil, errors.New("a type cannot be its own parent")
	}
	want := models.TicketLevelMain
	if t.Level == models.TicketLevelSubSub {
		want = models.TicketLevelSub
	}
	if p.Level != want {
		return nil, fmt.Errorf("the parent of a %s type must be a %s type; %s is %s",
			strings.ReplaceAll(t.Level, "_", "-"), want, p.Code, strings.ReplaceAll(p.Level, "_", "-"))
	}
	t.ParentTypeID = &p.ID
	return &p, nil
}

// validateReferencePattern allows {YYYY}, {N…}, {parent} and {#}.
//
//	main, universal → must have a counter {N…} and no {parent}
//	sub, sub_sub    → must start from {parent} and add {#} or a fixed suffix
func validateReferencePattern(level, p string) error {
	if p == "" {
		return errors.New("reference pattern is required")
	}
	for _, tok := range patternTokRe.FindAllString(p, -1) {
		if tok != "{YYYY}" && tok != "{parent}" && tok != "{#}" && !nTokenRe.MatchString(tok) {
			return fmt.Errorf("unknown token %s in reference pattern (allowed: {YYYY}, {NNN}, {parent}, {#})", tok)
		}
	}
	if strings.Count(p, "{") != strings.Count(p, "}") {
		return errors.New("reference pattern has an unclosed {")
	}
	counters := len(nTokenRe.FindAllString(p, -1))
	if counters > 1 {
		return errors.New("reference pattern may have only one counter {N…}")
	}
	switch level {
	case models.TicketLevelMain, models.TicketLevelUniversal:
		if strings.Contains(p, "{parent}") {
			return fmt.Errorf("a %s type has no parent, so its pattern cannot use {parent}", level)
		}
		if counters == 0 {
			return errors.New("the pattern needs a counter such as {NNN}, or references would repeat")
		}
	default:
		if !strings.HasPrefix(p, "{parent}") {
			return errors.New("a sub-type's pattern must start with {parent}, e.g. {parent}-A{#}")
		}
		if p == "{parent}" {
			return errors.New("the pattern must add something after {parent}, e.g. {parent}-A{#}")
		}
	}
	return nil
}

func resolveWorkflow(tx *gorm.DB, idStr string) (uuid.UUID, error) {
	id, err := uuid.Parse(idStr)
	if err != nil {
		return uuid.Nil, errors.New("invalid workflow_id")
	}
	var n int64
	tx.Model(&models.Workflow{}).Where("id = ?", id).Count(&n)
	if n == 0 {
		return uuid.Nil, errors.New("workflow not found")
	}
	return id, nil
}

func normaliseFields(tx *gorm.DB, fields []models.TicketFieldDef) (datatypes.JSON, error) {
	if fields == nil {
		fields = []models.TicketFieldDef{}
	}
	seen := map[string]bool{}
	allowed := map[string]bool{}
	for _, ft := range models.TicketFieldTypes {
		allowed[ft] = true
	}
	for i := range fields {
		f := &fields[i]
		f.Label = strings.TrimSpace(f.Label)
		f.Handle = strings.TrimSpace(f.Handle)
		if f.Label == "" {
			return nil, fmt.Errorf("field %d has no label", i+1)
		}
		if !fieldHandleRe.MatchString(f.Handle) {
			return nil, fmt.Errorf("field %q: handle must be lowercase letters, digits or _ (e.g. owning_body)", f.Label)
		}
		if seen[f.Handle] {
			return nil, fmt.Errorf("field handle %q is used twice", f.Handle)
		}
		seen[f.Handle] = true
		if !allowed[f.Type] {
			return nil, fmt.Errorf("field %q: type %q is not one of %s", f.Label, f.Type, strings.Join(models.TicketFieldTypes, ", "))
		}
		if f.Type == "select" && len(f.Options) == 0 {
			return nil, fmt.Errorf("field %q: a select needs at least one option", f.Label)
		}
		if f.Type == "linked_ticket_type" {
			f.LinkType = strings.ToUpper(strings.TrimSpace(f.LinkType))
			if f.LinkType == "" {
				return nil, fmt.Errorf("field %q: a linked ticket type field needs link_type", f.Label)
			}
			var n int64
			tx.Model(&models.TicketType{}).Where("code = ?", f.LinkType).Count(&n)
			if n == 0 {
				return nil, fmt.Errorf("field %q: ticket type %s not found", f.Label, f.LinkType)
			}
		}
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	return datatypes.JSON(raw), nil
}

// checkNoRemovedRequiredFieldsInUse stops a live type from silently dropping a
// field that existing tickets were created against. Adding fields is fine.
func checkNoRemovedRequiredFieldsInUse(oldRaw, newRaw datatypes.JSON) error {
	var oldF, newF []models.TicketFieldDef
	_ = json.Unmarshal(oldRaw, &oldF)
	_ = json.Unmarshal(newRaw, &newF)
	keep := map[string]models.TicketFieldDef{}
	for _, f := range newF {
		keep[f.Handle] = f
	}
	for _, f := range oldF {
		nf, ok := keep[f.Handle]
		if !ok {
			return fmt.Errorf("field %q cannot be removed from a live type", f.Handle)
		}
		if nf.Type != f.Type {
			return fmt.Errorf("field %q cannot change type on a live type", f.Handle)
		}
	}
	return nil
}

func ensureLink(tx *gorm.DB, parent, child *models.TicketType) error {
	var n int64
	tx.Model(&models.TicketTypeLink{}).Where("parent_type_id = ? AND child_type_id = ?", parent.ID, child.ID).Count(&n)
	if n > 0 {
		return nil
	}
	var max int
	tx.Model(&models.TicketTypeLink{}).Where("parent_type_id = ?", parent.ID).Select("COALESCE(MAX(sort_order), -1)").Scan(&max)
	return tx.Create(&models.TicketTypeLink{
		ParentTypeID: parent.ID, ChildTypeID: child.ID,
		Cardinality: "many", Behaviours: []string{}, SortOrder: max + 1,
	}).Error
}

func removeLink(tx *gorm.DB, parentID, childID uuid.UUID) error {
	if err := tx.Where("parent_type_id = ? AND child_type_id = ?", parentID, childID).Delete(&models.TicketTypeLink{}).Error; err != nil {
		return err
	}
	var p models.TicketType
	if tx.First(&p, "id = ?", parentID).Error == nil {
		return syncClosureRequirements(tx, &p)
	}
	return nil
}

func countTicketsOfType(tx *gorm.DB, typeID uuid.UUID) int64 {
	var n int64
	tx.Unscoped().Model(&models.Ticket{}).Where("ticket_type_id = ?", typeID).Count(&n)
	return n
}

func sameUUID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// goLiveChecks is the builder's "Before it can go live" list:
//   - at least one field
//   - a workflow with at least two states (an initial and a terminal one)
//   - a closure condition: a transition into a terminal state
//   - for a sub-type: its parent is live and the link is set on the parent
func goLiveChecks(tx *gorm.DB, t *models.TicketType) []models.GoLiveCheck {
	var checks []models.GoLiveCheck

	var fields []models.TicketFieldDef
	_ = json.Unmarshal(t.Fields, &fields)
	checks = append(checks, models.GoLiveCheck{
		Label: "At least one field", OK: len(fields) > 0,
		Hint: "Add at least one field on the Fields tab",
	})

	var states []models.WorkflowState
	if t.WorkflowID != nil {
		tx.Where("workflow_id = ? AND is_active = ?", *t.WorkflowID, true).Find(&states)
	}
	hasInitial, hasTerminal := false, false
	for _, st := range states {
		hasInitial = hasInitial || st.StateType == "initial"
		hasTerminal = hasTerminal || st.StateType == "terminal"
	}
	checks = append(checks, models.GoLiveCheck{
		Label: "At least two states", OK: t.WorkflowID != nil && len(states) >= 2 && hasInitial && hasTerminal,
		Hint: "Choose a workflow that has a start state and an end state",
	})

	closure := false
	if t.WorkflowID != nil {
		var n int64
		tx.Table("workflow_transitions t").
			Joins("JOIN workflow_states s ON s.id = t.to_state_id").
			Where("t.workflow_id = ? AND s.state_type = ? AND t.deleted_at IS NULL AND t.is_active = ?", *t.WorkflowID, "terminal", true).
			Count(&n)
		closure = n > 0
	}
	checks = append(checks, models.GoLiveCheck{
		Label: "A closure condition", OK: closure,
		Hint: "The workflow needs a step that moves the ticket into an end state",
	})

	if t.ParentTypeID != nil {
		var p models.TicketType
		parentOK := tx.First(&p, "id = ?", *t.ParentTypeID).Error == nil
		var n int64
		if parentOK {
			tx.Model(&models.TicketTypeLink{}).Where("parent_type_id = ? AND child_type_id = ?", p.ID, t.ID).Count(&n)
		}
		checks = append(checks, models.GoLiveCheck{
			Label: "Link behaviour set on the parent", OK: n > 0,
			Hint: "Link this type on " + p.Name + "'s Linked types tab",
		})
		checks = append(checks, models.GoLiveCheck{
			Label: "Parent type is live", OK: parentOK && p.Status == models.TicketTypeLive,
			Hint: "Publish " + p.Name + " first",
		})
	}
	return checks
}

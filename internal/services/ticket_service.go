package services

// POC — Ticket Type Builder (M1) on top of the existing workflow module.
//
// Tickets live in their own `tickets` table; parent_id builds the tree.
// Moves go through TicketService.Transition, using the shared workflow
// definitions (states, transitions, requirements, actions). Two workflow values
// carry the ticket rules:
//   - requirement_type "linked_closed": the move is refused while any child of
//     the configured type is not in a terminal state.
//   - action_type "create_linked": the move raises a child ticket.
// Saving a link with "blocks_parent_closure" writes the linked_closed
// requirement onto every transition of the parent's workflow that enters a
// terminal state.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type TicketService struct {
	db *gorm.DB
}

func NewTicketService(db *gorm.DB) *TicketService {
	return &TicketService{db: db}
}

// ---------------------------------------------------------------------------
// Ticket types
// ---------------------------------------------------------------------------

// ListTypes returns the types as a tree, flattened: each main type, then its
// sub-types indented under it, then universal types last.
func (s *TicketService) ListTypes(ctx context.Context, f *models.TicketTypeFilter) ([]models.TicketTypeRow, error) {
	var types []models.TicketType
	if err := s.db.WithContext(ctx).Order("sort_order, name").Find(&types).Error; err != nil {
		return nil, err
	}
	var links []models.TicketTypeLink
	if err := s.db.WithContext(ctx).Find(&links).Error; err != nil {
		return nil, err
	}
	linkCount := map[uuid.UUID]int{}
	for _, l := range links {
		linkCount[l.ParentTypeID]++
	}
	byID := map[uuid.UUID]models.TicketType{}
	children := map[uuid.UUID][]models.TicketType{}
	for _, t := range types {
		byID[t.ID] = t
		if t.ParentTypeID != nil {
			children[*t.ParentTypeID] = append(children[*t.ParentTypeID], t)
		}
	}

	rows := []models.TicketTypeRow{}
	var walk func(t models.TicketType, depth int)
	walk = func(t models.TicketType, depth int) {
		row := models.TicketTypeRow{
			ID: t.ID, Code: t.Code, Handle: t.Handle, Name: t.Name, NameAr: t.NameAr,
			Level: t.Level, Subsystem: t.Subsystem, ReferencePattern: displayPattern(t.ReferencePattern, t, byID),
			LinkedCount: linkCount[t.ID], Status: t.Status, Depth: depth,
		}
		if t.ParentTypeID != nil {
			row.ParentTypeName = byID[*t.ParentTypeID].Name
		} else if t.Level == models.TicketLevelUniversal {
			row.ParentTypeName = "Any type"
		}
		rows = append(rows, row)
		for _, c := range children[t.ID] {
			walk(c, depth+1)
		}
	}
	for _, t := range types {
		if t.Level == models.TicketLevelMain {
			walk(t, 0)
		}
	}
	for _, t := range types {
		if t.Level == models.TicketLevelUniversal {
			walk(t, 0)
		}
	}
	return filterTypeRows(rows, f), nil
}

// displayPattern expands {parent} into the parent type's pattern so the list
// shows EO-DIR-YYYY-NNN-A# rather than {parent}-A{#}.
func displayPattern(p string, t models.TicketType, byID map[uuid.UUID]models.TicketType) string {
	out := p
	for i := 0; i < 5 && strings.Contains(out, "{parent}") && t.ParentTypeID != nil; i++ {
		parent := byID[*t.ParentTypeID]
		out = strings.ReplaceAll(out, "{parent}", parent.ReferencePattern)
		t = parent
	}
	out = strings.ReplaceAll(out, "{YYYY}", "YYYY")
	out = strings.ReplaceAll(out, "{#}", "#")
	return nTokenRe.ReplaceAllStringFunc(out, func(m string) string { return strings.Trim(m, "{}") })
}

var nTokenRe = regexp.MustCompile(`\{N+\}`)

type TicketTypeDetail struct {
	Type  models.TicketType       `json:"type"`
	Links []models.TicketTypeLink `json:"links"`
}

func (s *TicketService) GetType(ctx context.Context, code string) (*TicketTypeDetail, error) {
	var t models.TicketType
	if err := s.db.WithContext(ctx).Preload("ParentType").Where("code = ?", strings.ToUpper(code)).First(&t).Error; err != nil {
		return nil, fmt.Errorf("ticket type %s not found", code)
	}
	var links []models.TicketTypeLink
	s.db.WithContext(ctx).Preload("ChildType").Where("parent_type_id = ?", t.ID).Order("sort_order").Find(&links)
	return &TicketTypeDetail{Type: t, Links: links}, nil
}

// SaveLink creates or updates the link parent → child and re-syncs the
// linked_closed requirements on the parent's workflow.
func (s *TicketService) SaveLink(ctx context.Context, parentCode string, req *models.TicketTypeLinkRequest) (*models.TicketTypeLink, error) {
	var out *models.TicketTypeLink
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var parent, child models.TicketType
		if err := tx.Where("code = ?", strings.ToUpper(parentCode)).First(&parent).Error; err != nil {
			return fmt.Errorf("parent type %s not found", parentCode)
		}
		if err := tx.Where("code = ?", strings.ToUpper(req.ChildTypeCode)).First(&child).Error; err != nil {
			return fmt.Errorf("child type %s not found", req.ChildTypeCode)
		}
		if child.Level == models.TicketLevelMain {
			return errors.New("a main type cannot be linked under another type")
		}
		var link models.TicketTypeLink
		err := tx.Where("parent_type_id = ? AND child_type_id = ?", parent.ID, child.ID).First(&link).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		link.ParentTypeID, link.ChildTypeID = parent.ID, child.ID
		link.Cardinality = req.Cardinality
		if link.Cardinality == "" {
			link.Cardinality = "many"
		}
		link.Behaviours = req.Behaviours
		link.DueWithinParent = req.DueWithinParent
		link.AutoCreateOnOpen = req.AutoCreateOnOpen
		link.Note = req.Note
		if err := tx.Save(&link).Error; err != nil {
			return err
		}
		if err := syncClosureRequirements(tx, &parent); err != nil {
			return err
		}
		link.ChildType = &child
		out = &link
		return nil
	})
	return out, err
}

// syncClosureRequirements rewrites the linked_closed requirements on every
// transition of the type's workflow that enters a terminal state, one per link
// that blocks parent closure. Other requirements on those transitions are kept.
func syncClosureRequirements(tx *gorm.DB, parent *models.TicketType) error {
	if parent.WorkflowID == nil {
		return nil
	}
	// Several types may share one workflow, so the requirements are rebuilt
	// from the links of every type that uses it, not just this one.
	var typeIDs []uuid.UUID
	if err := tx.Model(&models.TicketType{}).Where("workflow_id = ?", *parent.WorkflowID).Pluck("id", &typeIDs).Error; err != nil {
		return err
	}
	var links []models.TicketTypeLink
	if err := tx.Preload("ChildType").Where("parent_type_id IN ?", typeIDs).Order("sort_order").Find(&links).Error; err != nil {
		return err
	}
	var terminalTransitionIDs []uuid.UUID
	if err := tx.Table("workflow_transitions t").
		Joins("JOIN workflow_states s ON s.id = t.to_state_id").
		Where("t.workflow_id = ? AND s.state_type = ? AND t.deleted_at IS NULL", *parent.WorkflowID, "terminal").
		Pluck("t.id", &terminalTransitionIDs).Error; err != nil {
		return err
	}
	for _, tid := range terminalTransitionIDs {
		if err := tx.Where("transition_id = ? AND requirement_type = ?", tid, models.RequirementLinkedClosed).
			Delete(&models.TransitionRequirement{}).Error; err != nil {
			return err
		}
		seen := map[uuid.UUID]bool{} // one requirement per child type on each transition
		for _, l := range links {
			if !l.HasBehaviour(models.LinkBlocksParentClosure) || seen[l.ChildTypeID] {
				continue
			}
			seen[l.ChildTypeID] = true
			cfg, _ := json.Marshal(models.LinkedClosedConfig{Type: l.ChildType.Handle, Scope: "all", LinkID: l.ID.String()})
			mandatory := true
			r := models.TransitionRequirement{
				TransitionID:    tid,
				RequirementType: models.RequirementLinkedClosed,
				IsMandatory:     &mandatory,
				Config:          string(cfg),
				ErrorMessage:    fmt.Sprintf("Every %s must be closed first", l.ChildType.Name),
			}
			if err := tx.Create(&r).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// References
// ---------------------------------------------------------------------------

// nextReference builds the next reference for a ticket of type t under parent.
// When commit is false the counter is only read (used for "Next reference").
func nextReference(tx *gorm.DB, t *models.TicketType, parent *models.Ticket, now time.Time, commit bool) (string, error) {
	ref := t.ReferencePattern
	year := now.Year()
	ref = strings.ReplaceAll(ref, "{YYYY}", strconv.Itoa(year))

	if strings.Contains(ref, "{parent}") {
		if parent == nil {
			return "", fmt.Errorf("%s needs a parent", t.Name)
		}
		ref = strings.ReplaceAll(ref, "{parent}", parent.Reference)
	}
	if strings.Contains(ref, "{#}") {
		pos := 1
		if parent != nil {
			var n int64
			tx.Unscoped().Model(&models.Ticket{}).
				Where("parent_id = ? AND ticket_type_id = ?", parent.ID, t.ID).Count(&n)
			pos = int(n) + 1
		}
		ref = strings.ReplaceAll(ref, "{#}", strconv.Itoa(pos))
	}
	if m := nTokenRe.FindString(ref); m != "" {
		if commit {
			// Serialise numbering per type and year until this transaction ends,
			// so two requests can never take the same number.
			if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext(?))`,
				fmt.Sprintf("ticket-ref:%s:%d", t.Code, year)).Error; err != nil {
				return "", err
			}
		}
		last, err := lastCounterValue(tx, t, year)
		if err != nil {
			return "", err
		}
		width := len(m) - 2
		ref = strings.Replace(ref, m, fmt.Sprintf("%0*d", width, last+1), 1)
	}
	return ref, nil
}

// lastCounterValue returns the highest {N…} value this type has already used in
// a year. It reads the references themselves: the type's pattern becomes a regex
// with the counter as the only capture group. Soft-deleted tickets are included,
// so their numbers are never handed out again.
func lastCounterValue(tx *gorm.DB, t *models.TicketType, year int) (int, error) {
	re := patternRegex(t.ReferencePattern, year)
	if re == "" {
		return 0, nil
	}
	var n int
	err := tx.Raw(`SELECT COALESCE(MAX(substring(reference FROM ?)::int), 0)
		FROM tickets WHERE ticket_type_id = ? AND reference ~ ?`, re, t.ID, re).Scan(&n).Error
	return n, err
}

// patternRegex turns a reference pattern into an anchored PostgreSQL regex for
// one year, with the counter {N…} as the only capturing group.
// "MED-{YYYY}-{NNN}" → ^MED-2026-([0-9]{3,})$
func patternRegex(pattern string, year int) string {
	if !nTokenRe.MatchString(pattern) {
		return ""
	}
	var b strings.Builder
	b.WriteString("^")
	rest := pattern
	for rest != "" {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			b.WriteString(regexp.QuoteMeta(rest))
			break
		}
		b.WriteString(regexp.QuoteMeta(rest[:i]))
		j := strings.IndexByte(rest[i:], '}')
		if j < 0 {
			b.WriteString(regexp.QuoteMeta(rest[i:]))
			break
		}
		tok := rest[i : i+j+1]
		switch {
		case tok == "{YYYY}":
			b.WriteString(strconv.Itoa(year))
		case nTokenRe.MatchString(tok):
			fmt.Fprintf(&b, "([0-9]{%d,})", len(tok)-2)
		case tok == "{parent}":
			b.WriteString("(?:.+)")
		case tok == "{#}":
			b.WriteString("(?:[0-9]+)")
		default:
			b.WriteString(regexp.QuoteMeta(tok))
		}
		rest = rest[i+j+1:]
	}
	b.WriteString("$")
	return b.String()
}

// PreviewNextReference is the "Next reference" hint on the General tab.
func (s *TicketService) PreviewNextReference(ctx context.Context, code string) (string, error) {
	var t models.TicketType
	if err := s.db.WithContext(ctx).Where("code = ?", strings.ToUpper(code)).First(&t).Error; err != nil {
		return "", err
	}
	if strings.Contains(t.ReferencePattern, "{parent}") {
		return t.ReferencePattern, nil
	}
	return nextReference(s.db.WithContext(ctx), &t, nil, time.Now(), false)
}

// ---------------------------------------------------------------------------
// Tickets
// ---------------------------------------------------------------------------

func (s *TicketService) CreateTicket(ctx context.Context, req *models.TicketCreateRequest, userID uuid.UUID) (*models.Ticket, error) {
	var newInc *models.Ticket
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var parentID *uuid.UUID
		if req.ParentID != nil && *req.ParentID != "" {
			id, err := uuid.Parse(*req.ParentID)
			if err != nil {
				return errors.New("invalid parent_id")
			}
			parentID = &id
		}
		inc, err := createTicketTx(tx, req.TypeCode, parentID, req.Title, req.DueDate, req.Attributes, true, userID)
		if err != nil {
			return err
		}
		newInc = inc
		return nil
	})
	return newInc, err
}

// createTicketTx creates one ticket inside tx, enforcing the link rules that
// apply at creation: the child must be linked under the parent's type, a
// "one" link allows a single child, and a due date must fall within the
// parent's when the link says so.
func createTicketTx(tx *gorm.DB, typeCode string, parentID *uuid.UUID, title string, due *time.Time, attrs map[string]interface{}, checkRequired bool, userID uuid.UUID) (*models.Ticket, error) {
	var t models.TicketType
	if err := tx.Where("code = ? OR handle = ?", strings.ToUpper(typeCode), typeCode).First(&t).Error; err != nil {
		return nil, fmt.Errorf("ticket type %s not found", typeCode)
	}
	if t.Status != models.TicketTypeLive {
		return nil, fmt.Errorf("%s is not live yet; publish it before creating tickets of this type", t.Name)
	}
	if t.WorkflowID == nil {
		return nil, fmt.Errorf("%s has no workflow", t.Name)
	}

	var parent *models.Ticket
	if parentID != nil {
		var p models.Ticket
		// Lock the parent so sibling numbering ({#}) and "one per parent" are race-free.
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Preload("TicketType").First(&p, "id = ?", *parentID).Error; err != nil {
			return nil, errors.New("parent ticket not found")
		}
		parent = &p
	}

	switch t.Level {
	case models.TicketLevelMain:
		if parent != nil {
			return nil, fmt.Errorf("%s is a main type and opens its own case", t.Name)
		}
	case models.TicketLevelSub, models.TicketLevelSubSub:
		if parent == nil {
			return nil, fmt.Errorf("%s hangs off a parent", t.Name)
		}
	}

	if parent != nil {
		var link models.TicketTypeLink
		err := tx.Where("parent_type_id = ? AND child_type_id = ?", parent.TicketTypeID, t.ID).First(&link).Error
		if err != nil {
			if t.Level != models.TicketLevelUniversal {
				return nil, fmt.Errorf("%s cannot hang off a %s", t.Name, parent.TicketType.Name)
			}
			// A universal type may hang off anything even without an explicit link.
		} else {
			if link.Cardinality == "one" {
				var n int64
				tx.Model(&models.Ticket{}).Where("parent_id = ? AND ticket_type_id = ?", parent.ID, t.ID).Count(&n)
				if n > 0 {
					return nil, fmt.Errorf("only one %s per %s", t.Name, parent.TicketType.Name)
				}
			}
			if link.DueWithinParent && parent.DueDate != nil {
				if due == nil {
					d := *parent.DueDate
					due = &d
				} else if due.After(*parent.DueDate) {
					return nil, fmt.Errorf("deadline must fall on or before the parent's (%s)", parent.DueDate.Format("2 Jan 2006"))
				}
			}
		}
	}

	var initial models.WorkflowState
	if err := tx.Where("workflow_id = ? AND state_type = ?", *t.WorkflowID, "initial").Order("sort_order").First(&initial).Error; err != nil {
		return nil, fmt.Errorf("%s workflow has no initial state", t.Name)
	}

	ref, err := nextReference(tx, &t, parent, time.Now(), true)
	if err != nil {
		return nil, err
	}
	if attrs == nil {
		attrs = map[string]interface{}{}
	}
	if err := validateAttributes(tx, t.Fields, attrs, checkRequired); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(attrs)

	inc := &models.Ticket{
		TicketTypeID:   t.ID,
		Reference:      ref,
		Title:          title,
		ParentID:       parentID,
		WorkflowID:     *t.WorkflowID,
		CurrentStateID: initial.ID,
		DueDate:        due,
		Attributes:     datatypes.JSON(raw),
		CreatedByID:    &userID,
	}
	if parent != nil {
		root := parent.ID
		if parent.RootID != nil {
			root = *parent.RootID
		}
		inc.RootID = &root
	}
	if err := tx.Create(inc).Error; err != nil {
		return nil, err
	}

	if err := logActivity(tx, inc.ID, models.TicketActivityCreated, fmt.Sprintf("%s %s created in %s", t.Name, ref, initial.Name),
		map[string]interface{}{"reference": ref, "state": initial.Name}, userID); err != nil {
		return nil, err
	}
	if parent != nil {
		if err := logActivity(tx, parent.ID, models.TicketActivityChildCreated, fmt.Sprintf("%s %s created", t.Name, ref),
			map[string]interface{}{"child_id": inc.ID, "reference": ref}, userID); err != nil {
			return nil, err
		}
	}
	return inc, nil
}

func attrValue(raw datatypes.JSON, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]interface{}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	if v, ok := m[key]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

// ---------------------------------------------------------------------------
// Workflow rules used by TicketService.Transition
// ---------------------------------------------------------------------------

// CheckLinkedClosedAll checks every mandatory linked_closed requirement on a
// transition. It refuses the move while any child of those types is open:
// "Cannot close yet, open: Assignment 1, Task 2".
func CheckLinkedClosedAll(tx *gorm.DB, ticketID uuid.UUID, reqs []models.TransitionRequirement) error {
	var open []string
	for _, r := range reqs {
		if r.RequirementType != models.RequirementLinkedClosed || r.IsMandatory == nil || !*r.IsMandatory {
			continue
		}
		n, typeName, err := countOpenChildren(tx, ticketID, r.Config)
		if err != nil {
			return err
		}
		if n > 0 {
			open = append(open, fmt.Sprintf("%s %d", typeName, n))
		}
	}
	if len(open) == 0 {
		return nil
	}
	return fmt.Errorf("Cannot close yet, open: %s", strings.Join(open, ", "))
}

func countOpenChildren(tx *gorm.DB, ticketID uuid.UUID, cfgJSON string) (int, string, error) {
	var cfg models.LinkedClosedConfig
	if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil || cfg.Type == "" {
		return 0, "", errors.New("linked_closed requirement has no type configured")
	}
	var child models.TicketType
	if err := tx.Where("handle = ?", cfg.Type).First(&child).Error; err != nil {
		return 0, "", fmt.Errorf("linked type %s not found", cfg.Type)
	}
	var open int64
	if err := tx.Table("tickets t").
		Joins("JOIN workflow_states s ON s.id = t.current_state_id").
		Where("t.parent_id = ? AND t.ticket_type_id = ? AND t.deleted_at IS NULL AND s.state_type <> ?", ticketID, child.ID, "terminal").
		Count(&open).Error; err != nil {
		return 0, "", err
	}
	return int(open), child.Name, nil
}

// RunCreateLinkedActions raises the child tickets configured as create_linked
// actions on the transition. It runs inside the transition's transaction so a
// failure rolls the move back.
func RunCreateLinkedActions(tx *gorm.DB, parent *models.Ticket, actions []models.TransitionAction, userID uuid.UUID) error {
	sorted := append([]models.TransitionAction(nil), actions...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ExecutionOrder < sorted[j].ExecutionOrder })
	for _, a := range sorted {
		if a.ActionType != models.ActionCreateLinked || !a.IsActive {
			continue
		}
		var cfg models.CreateLinkedConfig
		if err := json.Unmarshal([]byte(a.Config), &cfg); err != nil || cfg.Type == "" {
			return fmt.Errorf("action %q has no ticket type configured", a.Name)
		}
		title := cfg.Title
		if title == "" {
			title = a.Name
		}
		title = strings.ReplaceAll(title, "{parent}", parent.Title)
		if len(title) > 200 {
			title = title[:200]
		}
		if _, err := createTicketTx(tx, cfg.Type, &parent.ID, title, nil, map[string]interface{}{}, false, userID); err != nil {
			return fmt.Errorf("%s: %w", a.Name, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Ticket detail (the Directive screen)
// ---------------------------------------------------------------------------

func (s *TicketService) GetTicket(ctx context.Context, id uuid.UUID) (*models.TicketDetail, error) {
	db := s.db.WithContext(ctx)
	var inc models.Ticket
	if err := db.Preload("TicketType").Preload("CurrentState").First(&inc, "id = ?", id).Error; err != nil {
		return nil, errors.New("ticket not found")
	}
	t := inc.TicketType
	d := &models.TicketDetail{
		ID: inc.ID, Reference: inc.Reference, Title: inc.Title,
		TypeCode: t.Code, TypeName: t.Name, Level: t.Level,
		Owner: ownerName(db, inc.Attributes), OwningBody: owningBodyName(db, inc.Attributes),
		Priority: attrValue(inc.Attributes, "priority"), DueDate: inc.DueDate,
		Version: inc.Version, Attributes: inc.Attributes,
		Groups: []models.TicketChildGroup{}, Checklist: []models.ChecklistItem{}, Transitions: []models.TransitionOption{},
	}
	if inc.CurrentState != nil {
		d.State, d.StateCode, d.StateColor = inc.CurrentState.Name, inc.CurrentState.Code, inc.CurrentState.Color
	}
	if inc.ParentID != nil {
		var p models.Ticket
		if db.Preload("CurrentState").First(&p, "id = ?", *inc.ParentID).Error == nil {
			row := childRow(db, &p)
			d.Parent = &row
		}
	}

	// Children, grouped by the links on this type.
	var links []models.TicketTypeLink
	db.Preload("ChildType").Where("parent_type_id = ?", t.ID).Order("sort_order").Find(&links)
	var kids []models.Ticket
	db.Preload("CurrentState").Where("parent_id = ?", inc.ID).Order("created_at").Find(&kids)

	var blocking []string
	for _, l := range links {
		g := models.TicketChildGroup{
			LinkID: l.ID, TypeCode: l.ChildType.Code, TypeName: l.ChildType.Name, Level: l.ChildType.Level,
			Behaviours: l.Behaviours, Cardinality: l.Cardinality,
			DueWithinParent: l.DueWithinParent,
			Children:        []models.TicketChildRow{},
		}
		for i := range kids {
			k := &kids[i]
			if k.TicketTypeID != l.ChildTypeID {
				continue
			}
			r := childRow(db, k)
			g.Children = append(g.Children, r)
			g.Total++
			if !r.Terminal {
				g.Open++
			}
		}
		d.Groups = append(d.Groups, g)

		// Closure checklist: one line per child type that blocks closing.
		if l.HasBehaviour(models.LinkBlocksParentClosure) {
			if g.Open > 0 {
				d.Checklist = append(d.Checklist, models.ChecklistItem{Label: fmt.Sprintf("%s: %d open", g.TypeName, g.Open), Status: "blocked"})
				blocking = append(blocking, fmt.Sprintf("%s %d", g.TypeName, g.Open))
			} else {
				d.Checklist = append(d.Checklist, models.ChecklistItem{Label: g.TypeName + ": all closed", Status: "done"})
			}
		}
	}

	// Transitions out of the current state, with what each one needs.
	var transitions []models.WorkflowTransition
	db.Preload("ToState").Preload("Requirements").
		Where("workflow_id = ? AND from_state_id = ? AND is_active = ?", inc.WorkflowID, inc.CurrentStateID, true).
		Order("sort_order").Find(&transitions)
	for _, tr := range transitions {
		opt := models.TransitionOption{ID: tr.ID, Name: tr.Name, Code: tr.Code}
		if tr.ToState != nil {
			opt.ToState = tr.ToState.Name
			opt.Terminal = tr.ToState.StateType == "terminal"
		}
		for _, r := range tr.Requirements {
			if r.IsMandatory == nil || !*r.IsMandatory {
				continue
			}
			switch r.RequirementType {
			case "comment":
				opt.NeedsComment = true
			case "feedback":
				opt.NeedsFeedback = true
			case "attachment":
				opt.NeedsAttachment = true
			case models.RequirementLinkedClosed:
				if n, name, err := countOpenChildren(db, inc.ID, r.Config); err == nil && n > 0 {
					opt.Blocked = true
					opt.Reasons = append(opt.Reasons, fmt.Sprintf("%s %d open", name, n))
				}
			}
		}
		// Can close = a move into a terminal state is available now and not blocked.
		if opt.Terminal && !opt.Blocked {
			d.CanClose = true
		}
		d.Transitions = append(d.Transitions, opt)
	}

	if len(blocking) > 0 {
		d.BlockReason = "Open: " + strings.Join(blocking, ", ")
	}
	return d, nil
}

// ownerName is the running owner's name when one is assigned (owner holds a
// user id), otherwise the owning body's department name.
func ownerName(db *gorm.DB, attrs datatypes.JSON) string {
	if v := attrValue(attrs, "owner"); v != "" {
		if _, err := uuid.Parse(v); err != nil {
			return v
		}
		return userDisplayName(db, v)
	}
	return owningBodyName(db, attrs)
}

// owningBodyName is the owning body's department name (owning_body holds its id).
func owningBodyName(db *gorm.DB, attrs datatypes.JSON) string {
	v := attrValue(attrs, "owning_body")
	if _, err := uuid.Parse(v); err != nil {
		return v
	}
	return departmentName(db, v)
}

func childRow(db *gorm.DB, k *models.Ticket) models.TicketChildRow {
	r := models.TicketChildRow{
		ID: k.ID, Reference: k.Reference, Title: k.Title,
		Owner: ownerName(db, k.Attributes), DueDate: k.DueDate,
		Origin: attrValue(k.Attributes, "origin"),
	}
	if k.CurrentState != nil {
		r.State, r.StateCode = k.CurrentState.Name, k.CurrentState.Code
		r.Terminal = k.CurrentState.StateType == "terminal"
	}
	if k.DueDate != nil && !r.Terminal && time.Now().After(*k.DueDate) {
		r.DaysLate = int(time.Since(*k.DueDate).Hours() / 24)
	}
	return r
}

// ---------------------------------------------------------------------------
// Moves (POST /tickets/:id/transition)
// ---------------------------------------------------------------------------

// Transition moves a ticket along one of its workflow's transitions. In one
// transaction it checks the version, the from-state, the roles and the
// requirements (comment, linked_closed), changes the state, writes the
// history and runs the create_linked actions.
func (s *TicketService) Transition(ctx context.Context, id uuid.UUID, req *models.TicketTransitionRequest, userID uuid.UUID) (*models.TicketDetail, error) {
	transitionID, err := uuid.Parse(req.TransitionID)
	if err != nil {
		return nil, errors.New("invalid transition_id")
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var t models.Ticket
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&t, "id = ?", id).Error; err != nil {
			return errors.New("ticket not found")
		}
		if t.Version != req.Version {
			return fmt.Errorf("the ticket was changed by someone else (version is %d); reload it and try again", t.Version)
		}

		var tr models.WorkflowTransition
		if err := tx.Preload("FromState").Preload("ToState").Preload("Requirements").Preload("Actions").Preload("AllowedRoles").
			First(&tr, "id = ? AND is_active = ?", transitionID, true).Error; err != nil {
			return errors.New("transition not found")
		}
		if tr.WorkflowID != t.WorkflowID {
			return errors.New("this transition belongs to another workflow")
		}
		if tr.FromStateID != t.CurrentStateID {
			return fmt.Errorf("%q is not available from the ticket's current state", tr.Name)
		}

		// Roles: empty means anyone may run the move.
		if len(tr.AllowedRoles) > 0 {
			var userRoleIDs []uuid.UUID
			tx.Table("user_roles").Where("user_id = ?", userID).Pluck("role_id", &userRoleIDs)
			allowed := false
			for _, r := range tr.AllowedRoles {
				for _, ur := range userRoleIDs {
					if r.ID == ur {
						allowed = true
					}
				}
			}
			if !allowed {
				return fmt.Errorf("you are not allowed to run %q", tr.Name)
			}
		}

		// Requirements.
		comment := strings.TrimSpace(req.Comment)
		for _, r := range tr.Requirements {
			if r.IsMandatory == nil || !*r.IsMandatory {
				continue
			}
			switch r.RequirementType {
			case "comment", "feedback":
				if comment == "" {
					msg := r.ErrorMessage
					if msg == "" {
						msg = "a comment is required for " + tr.Name
					}
					return errors.New(msg)
				}
			}
		}
		if err := CheckLinkedClosedAll(tx, t.ID, tr.Requirements); err != nil {
			return err
		}

		// Move.
		fromState := t.CurrentStateID
		updates := map[string]interface{}{
			"current_state_id": tr.ToStateID,
			"version":          t.Version + 1,
			"updated_at":       time.Now(),
			"closed_at":        nil,
		}
		if tr.ToState != nil && tr.ToState.StateType == "terminal" {
			updates["closed_at"] = time.Now()
		}
		if err := tx.Model(&models.Ticket{}).Where("id = ?", t.ID).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.TicketTransition{
			TicketID: t.ID, TransitionID: &tr.ID, FromStateID: fromState, ToStateID: tr.ToStateID,
			PerformedByID: userID, Comment: comment,
		}).Error; err != nil {
			return err
		}
		fromName, toName := "", ""
		if tr.FromState != nil {
			fromName = tr.FromState.Name
		}
		if tr.ToState != nil {
			toName = tr.ToState.Name
		}
		if err := logActivity(tx, t.ID, models.TicketActivityTransitioned, fmt.Sprintf("%s: %s → %s", tr.Name, fromName, toName),
			map[string]interface{}{"transition": tr.Name, "from_state": fromName, "to_state": toName, "comment": comment}, userID); err != nil {
			return err
		}
		return RunCreateLinkedActions(tx, &t, tr.Actions, userID)
	})
	if err != nil {
		return nil, err
	}
	return s.GetTicket(ctx, id)
}

// ---------------------------------------------------------------------------
// List and history
// ---------------------------------------------------------------------------

// History returns a ticket's moves, oldest first.
func (s *TicketService) History(ctx context.Context, id uuid.UUID) ([]models.TicketHistoryRow, error) {
	rows := []models.TicketHistoryRow{}
	err := s.db.WithContext(ctx).Table("ticket_transitions h").
		Select(`COALESCE(wt.name, '') AS transition, fs.name AS from_state, ts.name AS to_state,
			COALESCE(NULLIF(TRIM(u.first_name || ' ' || u.last_name), ''), u.username, '') AS performed_by,
			h.comment, h.created_at AS at`).
		Joins("LEFT JOIN workflow_transitions wt ON wt.id = h.transition_id").
		Joins("JOIN workflow_states fs ON fs.id = h.from_state_id").
		Joins("JOIN workflow_states ts ON ts.id = h.to_state_id").
		Joins("LEFT JOIN users u ON u.id = h.performed_by_id").
		Where("h.ticket_id = ?", id).
		Order("h.created_at").Scan(&rows).Error
	return rows, err
}

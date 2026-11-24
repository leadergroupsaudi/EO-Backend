package services

// POC seed: the ticket types, workflows and links from
// "Ticket Type Builder Screens.pdf". Idempotent — runs at every startup.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type seedState struct {
	code, name, typ, color string
	slaDays                int
}

type seedReq struct {
	typ       string
	mandatory bool
	msg       string
}

type seedAction struct {
	typ, name, config string
}

type seedTransition struct {
	code, name, from, to string
	reqs                 []seedReq
	actions              []seedAction
	reopen               bool
}

func seedWorkflow(tx *gorm.DB, code, name, recordType string, states []seedState, transitions []seedTransition) (uuid.UUID, error) {
	var wf models.Workflow
	if err := tx.Where("code = ?", code).First(&wf).Error; err == nil {
		return wf.ID, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return uuid.Nil, err
	}
	wf = models.Workflow{Name: name, Code: code, RecordType: recordType, IsActive: true,
		Description: "Ticket POC workflow", Sources: "[]", Priorities: "[]"}
	if err := tx.Create(&wf).Error; err != nil {
		return uuid.Nil, err
	}
	ids := map[string]uuid.UUID{}
	for i, s := range states {
		st := models.WorkflowState{WorkflowID: wf.ID, Code: s.code, Name: s.name, StateType: s.typ, Color: s.color, SortOrder: i, IsActive: true, PositionX: 220 * i}
		if s.slaDays > 0 {
			d := s.slaDays
			st.SLAHours, st.SLAUnit = &d, "days"
		}
		if err := tx.Create(&st).Error; err != nil {
			return uuid.Nil, err
		}
		ids[s.code] = st.ID
	}
	for i, t := range transitions {
		tr := models.WorkflowTransition{WorkflowID: wf.ID, Code: t.code, Name: t.name,
			FromStateID: ids[t.from], ToStateID: ids[t.to], SortOrder: i, IsActive: true, IsReopen: t.reopen}
		if err := tx.Create(&tr).Error; err != nil {
			return uuid.Nil, err
		}
		for _, r := range t.reqs {
			m := r.mandatory
			if err := tx.Create(&models.TransitionRequirement{TransitionID: tr.ID, RequirementType: r.typ, IsMandatory: &m, ErrorMessage: r.msg}).Error; err != nil {
				return uuid.Nil, err
			}
		}
		for j, a := range t.actions {
			if err := tx.Create(&models.TransitionAction{TransitionID: tr.ID, ActionType: a.typ, Name: a.name, Config: a.config, ExecutionOrder: j + 1, IsActive: true}).Error; err != nil {
				return uuid.Nil, err
			}
		}
	}
	return wf.ID, nil
}

type seedType struct {
	code, handle, name, nameAr, level, subsystem, parent, pattern, status, workflow string
	fields                                                                          []map[string]interface{}
}

func field(label, handle, typ string, required, multi bool) map[string]interface{} {
	return map[string]interface{}{"label": label, "handle": handle, "type": typ, "required": required, "multi": multi}
}

// directiveFields: the five details captured at registration (AMAX-4702) are
// required; the assignment details (AMAX-4703) are set later through
// PUT /tickets/:id/assignment, which requires them there.
func directiveFields() []map[string]interface{} {
	return []map[string]interface{}{
		// Registration
		field("Directive text", "subject", "text", true, false),
		field("Expected outcome", "expected_outcome", "text", true, false),
		selectField("Source", "source", true, "meeting", "direct"),
		field("Directive date", "directive_date", "date_hijri_gregorian", true, false),
		selectField("Sensitivity", "classification", true, "public", "internal", "confidential", "highly_confidential"),
		field("Issued by", "issued_by", "user", false, false),
		field("Owning body", "owning_body", "org_unit", false, false),
		field("Owner", "owner", "user", false, false),
		selectField("Priority", "priority", false, "low", "medium", "high", "urgent"),
		field("Due date", "due_date", "date_hijri_gregorian", false, false),
		field("Expected back", "expected_back", "text", false, false),
		field("Evidence", "evidence", "file", false, true),
		field("Execution plan", "execution_plan", "linked_ticket_type", false, false),
		field("Assignments", "assignments", "linked_ticket_type", false, true),
	}
}

func selectField(label, handle string, required bool, options ...string) map[string]interface{} {
	f := field(label, handle, "select", required, false)
	f["options"] = options
	return f
}

// SeedTicketTypes creates the POC ticket types, their workflows and links.
// Runs at startup and is idempotent: anything that already exists is left
// as it is, so changes made through the API are not overwritten.
func (s *TicketService) SeedTicketTypes(ctx context.Context) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// --- Workflows ---------------------------------------------------
		dirWF, err := seedWorkflow(tx, "directive-management", "Directive management", "directive",
			[]seedState{
				{"registered", "Registered", "initial", "#64748b", 1},
				{"planning", "Planning", "normal", "#6366f1", 5},
				{"assigned", "Assigned", "normal", "#0ea5e9", 3},
				{"in_execution", "In execution", "normal", "#d97706", 0},
				{"returned", "Returned", "normal", "#dc2626", 2},
				{"verification", "Verification", "normal", "#7c3aed", 3},
				{"closed", "Closed", "terminal", "#16a34a", 0},
			},
			[]seedTransition{
				{code: "needs_plan", name: "Needs a plan", from: "registered", to: "planning",
					actions: []seedAction{{models.ActionCreateLinked, "Raise an Execution Plan", `{"type":"execution_plan","title":"Execution plan: {parent}"}`}}},
				{code: "approve_plan", name: "Approve plan", from: "planning", to: "assigned", reqs: []seedReq{{"comment", false, ""}}},
				{code: "send_back", name: "Send back", from: "planning", to: "returned", reqs: []seedReq{{"comment", true, "A comment is required to send back"}}},
				{code: "start_execution", name: "Start execution", from: "assigned", to: "in_execution"},
				{code: "submit_verification", name: "Submit for verification", from: "in_execution", to: "verification", reqs: []seedReq{{"comment", false, ""}}},
				{code: "final_close", name: "Final close", from: "verification", to: "closed",
					reqs: []seedReq{{"feedback", true, "A closing note from the Office is required"}, {"rating", false, ""}}},
				{code: "reopen", name: "Reopen", from: "closed", to: "in_execution", reopen: true,
					reqs: []seedReq{{"feedback", true, "A reason is required to reopen"}}},
			})
		if err != nil {
			return err
		}
		planWF, err := seedWorkflow(tx, "execution-plan", "Execution plan", "execution_plan",
			[]seedState{{"draft", "Draft", "initial", "#64748b", 5}, {"submitted", "Submitted", "normal", "#6366f1", 3}, {"approved", "Approved", "terminal", "#16a34a", 0}},
			[]seedTransition{
				{code: "submit", name: "Submit plan", from: "draft", to: "submitted"},
				{code: "approve", name: "Approve", from: "submitted", to: "approved"},
				{code: "return", name: "Return", from: "submitted", to: "draft", reqs: []seedReq{{"comment", true, "Say what to change"}}},
			})
		if err != nil {
			return err
		}
		asgWF, err := seedWorkflow(tx, "assignment", "Assignment", "assignment",
			[]seedState{{"open", "Open", "initial", "#64748b", 0}, {"in_progress", "In progress", "normal", "#d97706", 0}, {"submitted", "Submitted", "normal", "#6366f1", 3}, {"verified", "Verified", "terminal", "#16a34a", 0}},
			[]seedTransition{
				{code: "start", name: "Start", from: "open", to: "in_progress"},
				{code: "submit", name: "Submit", from: "in_progress", to: "submitted"},
				{code: "verify", name: "Verify", from: "submitted", to: "verified"},
				{code: "return", name: "Return", from: "submitted", to: "in_progress", reqs: []seedReq{{"comment", true, "Say what is missing"}}},
			})
		if err != nil {
			return err
		}
		tskWF, err := seedWorkflow(tx, "task", "Task", "task",
			[]seedState{{"open", "Open", "initial", "#d97706", 0}, {"done", "Done", "terminal", "#16a34a", 0}},
			[]seedTransition{{code: "complete", name: "Complete", from: "open", to: "done"}})
		if err != nil {
			return err
		}
		basicWF, err := seedWorkflow(tx, "ticket-basic", "Basic ticket (draft types)", "ticket",
			[]seedState{{"open", "Open", "initial", "#64748b", 0}, {"closed", "Closed", "terminal", "#16a34a", 0}},
			[]seedTransition{{code: "close", name: "Close", from: "open", to: "closed"}})
		if err != nil {
			return err
		}

		// --- Types ------------------------------------------------------
		types := []seedType{
			{"DIR", "directive", "Directive", "توجيه", models.TicketLevelMain, "EO-DIR", "", "EO-DIR-{YYYY}-{NNN}", models.TicketTypeLive, "dir", directiveFields()},
			{"PLN", "execution_plan", "Execution Plan", "خطة التنفيذ", models.TicketLevelSub, "EO-DIR", "DIR", "{parent}-P", models.TicketTypeLive, "plan", nil},
			{"ASG", "assignment", "Assignment", "تكليف", models.TicketLevelSub, "EO-DIR", "DIR", "{parent}-A{#}", models.TicketTypeLive, "asg", nil},
			{"CMT", "committee", "Committee", "لجنة", models.TicketLevelMain, "EO-CMT", "", "CMT-{YYYY}-{NN}", models.TicketTypeDraft, "basic", nil},
			{"MTG", "meeting", "Meeting", "اجتماع", models.TicketLevelSub, "EO-CMT", "CMT", "{parent}-M{#}", models.TicketTypeDraft, "basic", nil},
			{"CAI", "agenda_item", "Agenda Item", "بند جدول الأعمال", models.TicketLevelSubSub, "EO-CMT", "MTG", "{parent}-I{#}", models.TicketTypeDraft, "basic", nil},
			{"COM", "incoming_corr", "Incoming Correspondence", "مراسلة واردة", models.TicketLevelMain, "EO-COM", "", "IN-{YYYY}-{NNNN}", models.TicketTypeNotStarted, "", nil},
			{"TSK", "task", "Task", "مهمة", models.TicketLevelUniversal, "ALL", "", "TSK-{YYYY}-{NNNNN}", models.TicketTypeLive, "tsk", nil},
		}
		wfByKey := map[string]uuid.UUID{"dir": dirWF, "plan": planWF, "asg": asgWF, "tsk": tskWF, "basic": basicWF}
		typeIDs := map[string]uuid.UUID{}
		for i, st := range types {
			var t models.TicketType
			if err := tx.Where("code = ?", st.code).First(&t).Error; err == nil {
				typeIDs[st.code] = t.ID
				continue
			}
			t = models.TicketType{Code: st.code, Handle: st.handle, Name: st.name, NameAr: st.nameAr, Level: st.level,
				Subsystem: st.subsystem, ReferencePattern: st.pattern, Status: st.status, SortOrder: i}
			if st.parent != "" {
				pid := typeIDs[st.parent]
				t.ParentTypeID = &pid
			}
			if id, ok := wfByKey[st.workflow]; ok {
				t.WorkflowID = &id
			}
			if st.fields != nil {
				raw, _ := json.Marshal(st.fields)
				t.Fields = datatypes.JSON(raw)
			}
			if err := tx.Create(&t).Error; err != nil {
				return err
			}
			typeIDs[st.code] = t.ID
		}
		return nil
	})
	if err != nil {
		return err
	}

	// --- Links (through SaveLink so the closure requirements get written) ---
	links := []struct {
		parent string
		req    models.TicketTypeLinkRequest
	}{
		{"DIR", models.TicketTypeLinkRequest{ChildTypeCode: "PLN", Cardinality: "one", Behaviours: []string{models.LinkBlocksParentClosure}, Note: "Only where a plan is needed"}},
		{"DIR", models.TicketTypeLinkRequest{ChildTypeCode: "ASG", Cardinality: "many", Behaviours: []string{models.LinkBlocksParentClosure, models.LinkDeadlineFromParent}, DueWithinParent: true, Note: "Deadline must fall before the parent's"}},
		{"DIR", models.TicketTypeLinkRequest{ChildTypeCode: "TSK", Cardinality: "many", Behaviours: []string{models.LinkBlocksParentClosure}, Note: "Accepts children from other subsystems"}},
		{"ASG", models.TicketTypeLinkRequest{ChildTypeCode: "TSK", Cardinality: "many", Behaviours: []string{models.LinkBlocksParentClosure}}},
		{"CMT", models.TicketTypeLinkRequest{ChildTypeCode: "MTG", Cardinality: "many", Behaviours: []string{models.LinkRepeatingCycle}}},
		{"CMT", models.TicketTypeLinkRequest{ChildTypeCode: "TSK", Cardinality: "many", Behaviours: []string{models.LinkBlocksParentClosure}}},
		{"MTG", models.TicketTypeLinkRequest{ChildTypeCode: "CAI", Cardinality: "many", Behaviours: []string{models.LinkBlocksParentClosure}}},
		{"MTG", models.TicketTypeLinkRequest{ChildTypeCode: "TSK", Cardinality: "many", Behaviours: []string{models.LinkSpawnsCrossSystem}}},
	}
	for i, l := range links {
		var n int64
		s.db.WithContext(ctx).Model(&models.TicketTypeLink{}).
			Joins("JOIN ticket_types p ON p.id = ticket_type_links.parent_type_id").
			Joins("JOIN ticket_types c ON c.id = ticket_type_links.child_type_id").
			Where("p.code = ? AND c.code = ?", l.parent, l.req.ChildTypeCode).Count(&n)
		if n > 0 {
			continue
		}
		req := l.req
		saved, err := s.SaveLink(ctx, l.parent, &req)
		if err != nil {
			return fmt.Errorf("link %s→%s: %w", l.parent, l.req.ChildTypeCode, err)
		}
		s.db.WithContext(ctx).Model(saved).Update("sort_order", i)
	}
	return nil
}

package models

// POC — Ticket Type Builder (milestone M1).
//
// A ticket type is a configured kind of record (Directive, Execution Plan,
// Assignment, Task …). Tickets are stored in the `tickets` table (see
// ticket.go); parent_id builds the tree. Each type points at a workflow, so
// states, transitions, roles, requirements and actions are reused as-is.
//
// A link between two types carries its behaviour. Saving a link that blocks
// parent closure writes a `linked_closed` requirement onto every transition of
// the parent's workflow that enters a terminal state — there is no separate
// closure-gate mechanism.

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Ticket type levels.
const (
	TicketLevelMain      = "main"
	TicketLevelSub       = "sub"
	TicketLevelSubSub    = "sub_sub"
	TicketLevelUniversal = "universal"
)

// Ticket type status.
const (
	TicketTypeDraft      = "draft"
	TicketTypeLive       = "live"
	TicketTypeNotStarted = "not_started"
)

// Link behaviours (the seven drawn from the flowcharts).
const (
	LinkBlocksParentClosure = "blocks_parent_closure"
	LinkBlocksEarlierStep   = "blocks_earlier_step"
	LinkClosesParent        = "closes_parent"
	LinkDeadlineFromParent  = "deadline_from_parent"
	LinkRepeatingCycle      = "repeating_cycle"
	LinkFallbackSibling     = "fallback_sibling"
	LinkSpawnsCrossSystem   = "spawns_cross_subsystem"
)

// Requirement / action types added to the workflow module.
const (
	RequirementLinkedClosed = "linked_closed"
	ActionCreateLinked      = "create_linked"
)

// TicketType is one row in the Ticket types list.
type TicketType struct {
	ID        uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	Code      string    `gorm:"size:20;not null;uniqueIndex" json:"code"`   // DIR, PLN, ASG, TSK …
	Handle    string    `gorm:"size:50;not null;uniqueIndex" json:"handle"` // directive, execution_plan …
	Name      string    `gorm:"size:100;not null" json:"name"`
	NameAr    string    `gorm:"size:100" json:"name_ar"`
	Level     string    `gorm:"size:20;not null" json:"level"`     // main, sub, sub_sub, universal
	Subsystem string    `gorm:"size:20;not null" json:"subsystem"` // EO-DIR, EO-CMT, EO-COM, EO-MED, EO-VIS, EO-ACC, ALL

	ParentTypeID *uuid.UUID  `gorm:"type:uuid;index" json:"parent_type_id"`
	ParentType   *TicketType `gorm:"foreignKey:ParentTypeID" json:"parent_type,omitempty"`

	ReferencePattern string `gorm:"size:100;not null" json:"reference_pattern"`

	WorkflowID *uuid.UUID `gorm:"type:uuid;index" json:"workflow_id"`
	Workflow   *Workflow  `gorm:"foreignKey:WorkflowID" json:"workflow,omitempty"`

	Fields    datatypes.JSON `gorm:"type:jsonb" json:"fields"` // field definitions (label, handle, type, required, multi)
	Status    string         `gorm:"size:20;default:'draft'" json:"status"`
	SortOrder int            `gorm:"default:0" json:"sort_order"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

func (t *TicketType) BeforeCreate(tx *gorm.DB) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	return nil
}

// TicketTypeLink is a row on the parent's Linked types tab.
type TicketTypeLink struct {
	ID           uuid.UUID   `gorm:"type:uuid;primary_key" json:"id"`
	ParentTypeID uuid.UUID   `gorm:"type:uuid;not null;uniqueIndex:idx_ttl_parent_child" json:"parent_type_id"`
	ParentType   *TicketType `gorm:"foreignKey:ParentTypeID" json:"parent_type,omitempty"`
	ChildTypeID  uuid.UUID   `gorm:"type:uuid;not null;uniqueIndex:idx_ttl_parent_child" json:"child_type_id"`
	ChildType    *TicketType `gorm:"foreignKey:ChildTypeID" json:"child_type,omitempty"`

	// How many: "one" or "many".
	Cardinality string `gorm:"size:10;default:'many'" json:"cardinality"`
	// Behaviours: subset of the Link* constants. A link may carry more than one.
	Behaviours datatypes.JSONSlice[string] `gorm:"type:jsonb" json:"behaviours"`

	// "Also on this link"
	DueWithinParent  bool   `gorm:"default:false" json:"due_within_parent"`
	AutoCreateOnOpen bool   `gorm:"default:false" json:"auto_create_on_open"`
	Note             string `gorm:"size:300" json:"note"`

	SortOrder int       `gorm:"default:0" json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (l *TicketTypeLink) BeforeCreate(tx *gorm.DB) error {
	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	return nil
}

// HasBehaviour reports whether the link carries the given behaviour.
func (l *TicketTypeLink) HasBehaviour(b string) bool {
	for _, x := range l.Behaviours {
		if x == b {
			return true
		}
	}
	return false
}

// LinkedClosedConfig is the JSON stored in TransitionRequirement.Config for
// requirement_type = linked_closed.
type LinkedClosedConfig struct {
	Type   string `json:"type"`              // child ticket type handle
	Scope  string `json:"scope"`             // "all" (every child must be terminal)
	LinkID string `json:"link_id,omitempty"` // the link that wrote this requirement
}

// CreateLinkedConfig is the JSON stored in TransitionAction.Config for
// action_type = create_linked.
type CreateLinkedConfig struct {
	Type  string `json:"type"`            // child ticket type handle
	Title string `json:"title,omitempty"` // default title; "{parent}" is replaced by the parent's title
}

// ---- API shapes ----

type TicketTypeLinkRequest struct {
	ChildTypeCode    string   `json:"child_type_code" validate:"required"`
	Cardinality      string   `json:"cardinality" validate:"omitempty,oneof=one many"`
	Behaviours       []string `json:"behaviours"`
	DueWithinParent  bool     `json:"due_within_parent"`
	AutoCreateOnOpen bool     `json:"auto_create_on_open"`
	Note             string   `json:"note"`
}

// Field types a ticket type field may use (the Fields tab in the builder).
var TicketFieldTypes = []string{
	"text", "number", "date", "date_hijri_gregorian", "select", "checkbox",
	"user", "org_unit", "file", "linked_ticket_type",
}

// TicketFieldDef is one entry in TicketType.Fields.
type TicketFieldDef struct {
	Label    string   `json:"label" validate:"required,max=100"`
	LabelAr  string   `json:"label_ar,omitempty" validate:"omitempty,max=100"`
	Handle   string   `json:"handle" validate:"required,max=50"`
	Type     string   `json:"type" validate:"required"`
	Required bool     `json:"required"`
	Multi    bool     `json:"multi"`
	Options  []string `json:"options,omitempty"`   // for select
	LinkType string   `json:"link_type,omitempty"` // for linked_ticket_type: the child type code
	Help     string   `json:"help,omitempty" validate:"omitempty,max=300"`
}

// TicketTypeCreateRequest is the body of POST /ticket-types.
type TicketTypeCreateRequest struct {
	Code             string           `json:"code" validate:"required,min=2,max=10"`
	Handle           string           `json:"handle" validate:"required,min=2,max=20"`
	Name             string           `json:"name" validate:"required,max=100"`
	NameAr           string           `json:"name_ar" validate:"omitempty,max=100"`
	Level            string           `json:"level" validate:"required,oneof=main sub sub_sub universal"`
	Subsystem        string           `json:"subsystem" validate:"omitempty,max=20"`
	ParentTypeCode   string           `json:"parent_type_code" validate:"omitempty,max=10"`
	ReferencePattern string           `json:"reference_pattern" validate:"required,max=100"`
	WorkflowID       *string          `json:"workflow_id" validate:"omitempty,uuid"`
	Fields           []TicketFieldDef `json:"fields" validate:"omitempty,dive"`
	SortOrder        *int             `json:"sort_order"`
}

// TicketTypeUpdateRequest is the body of PUT /ticket-types/:code. Only the
// fields that are present are changed. Code, handle, level and parent type
// cannot change once the type is live.
type TicketTypeUpdateRequest struct {
	Code             *string `json:"code" validate:"omitempty,min=2,max=10"`
	Handle           *string `json:"handle" validate:"omitempty,min=2,max=20"`
	Name             *string `json:"name" validate:"omitempty,max=100"`
	NameAr           *string `json:"name_ar" validate:"omitempty,max=100"`
	Level            *string `json:"level" validate:"omitempty,oneof=main sub sub_sub universal"`
	Subsystem        *string `json:"subsystem" validate:"omitempty,max=20"`
	ParentTypeCode   *string `json:"parent_type_code" validate:"omitempty,max=10"`
	ReferencePattern *string `json:"reference_pattern" validate:"omitempty,max=100"`
	WorkflowID       *string `json:"workflow_id" validate:"omitempty,uuid"`
	SortOrder        *int    `json:"sort_order"`
}

// TicketTypeFieldsRequest is the body of PUT /ticket-types/:code/fields.
type TicketTypeFieldsRequest struct {
	Fields []TicketFieldDef `json:"fields" validate:"dive"`
}

// GoLiveCheck is one line of the "Before it can go live" list.
type GoLiveCheck struct {
	Label string `json:"label"`
	OK    bool   `json:"ok"`
	Hint  string `json:"hint,omitempty"`
}

type TicketCreateRequest struct {
	TypeCode   string                 `json:"type_code" validate:"required"`
	ParentID   *string                `json:"parent_id" validate:"omitempty,uuid"`
	Title      string                 `json:"title" validate:"required,max=200"`
	DueDate    *time.Time             `json:"due_date"`
	Attributes map[string]interface{} `json:"attributes"`
}

type TicketTypeRow struct {
	ID               uuid.UUID `json:"id"`
	Code             string    `json:"code"`
	Handle           string    `json:"handle"`
	Name             string    `json:"name"`
	NameAr           string    `json:"name_ar"`
	Level            string    `json:"level"`
	Subsystem        string    `json:"subsystem"`
	ParentTypeName   string    `json:"parent_type_name"`
	ReferencePattern string    `json:"reference_pattern"`
	LinkedCount      int       `json:"linked_count"`
	Status           string    `json:"status"`
	Depth            int       `json:"depth"`
	NextReference    string    `json:"next_reference"`
}

type TicketChildRow struct {
	ID        uuid.UUID  `json:"id"`
	Reference string     `json:"reference"`
	Title     string     `json:"title"`
	State     string     `json:"state"`
	StateCode string     `json:"state_code"`
	Terminal  bool       `json:"terminal"`
	Owner     string     `json:"owner"`
	DueDate   *time.Time `json:"due_date"`
	DaysLate  int        `json:"days_late"`
	Origin    string     `json:"origin,omitempty"` // reference of the record it came from, when cross-subsystem
}

type TicketChildGroup struct {
	LinkID          uuid.UUID        `json:"link_id"`
	TypeCode        string           `json:"type_code"`
	TypeName        string           `json:"type_name"`
	Level           string           `json:"level"`
	Behaviours      []string         `json:"behaviours"`
	Cardinality     string           `json:"cardinality"`
	DueWithinParent bool             `json:"due_within_parent"`
	Total           int              `json:"total"`
	Open            int              `json:"open"`
	Children        []TicketChildRow `json:"children"`
}

type ChecklistItem struct {
	Label  string `json:"label"`
	Status string `json:"status"` // done, blocked, pending
}

type TransitionOption struct {
	ID              uuid.UUID `json:"id"`
	Name            string    `json:"name"`
	Code            string    `json:"code"`
	ToState         string    `json:"to_state"`
	Terminal        bool      `json:"terminal"`
	Blocked         bool      `json:"blocked"`
	Reasons         []string  `json:"reasons,omitempty"`
	NeedsComment    bool      `json:"needs_comment"`
	NeedsFeedback   bool      `json:"needs_feedback"`
	NeedsAttachment bool      `json:"needs_attachment"`
}

type TicketDetail struct {
	ID          uuid.UUID          `json:"id"`
	Reference   string             `json:"reference"`
	Title       string             `json:"title"`
	TypeCode    string             `json:"type_code"`
	TypeName    string             `json:"type_name"`
	Level       string             `json:"level"`
	State       string             `json:"state"`
	StateCode   string             `json:"state_code"`
	StateColor  string             `json:"state_color"`
	Owner       string             `json:"owner"`
	OwningBody  string             `json:"owning_body"`
	Priority    string             `json:"priority"`
	DueDate     *time.Time         `json:"due_date"`
	Version     int                `json:"version"`
	Attributes  datatypes.JSON     `json:"attributes"`
	Parent      *TicketChildRow    `json:"parent,omitempty"`
	CanClose    bool               `json:"can_close"`
	BlockReason string             `json:"block_reason,omitempty"`
	Groups      []TicketChildGroup `json:"groups"`
	Checklist   []ChecklistItem    `json:"checklist"`
	Transitions []TransitionOption `json:"transitions"`
}

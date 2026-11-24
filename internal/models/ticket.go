package models

// Tickets: the records created from ticket types (Directive, Assignment, Task …).
// They have their own table and their own move flow; workflows (states,
// transitions, requirements, actions) are shared with the workflow module.

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Ticket is one record of a ticket type.
type Ticket struct {
	ID           uuid.UUID   `gorm:"type:uuid;primary_key" json:"id"`
	TicketTypeID uuid.UUID   `gorm:"type:uuid;not null;index" json:"ticket_type_id"`
	TicketType   *TicketType `gorm:"foreignKey:TicketTypeID" json:"ticket_type,omitempty"`
	Reference    string      `gorm:"size:60;not null;uniqueIndex" json:"reference"` // EO-DIR-2026-001, …-A1
	Title        string      `gorm:"size:200;not null" json:"title"`

	// The tree: ParentID is the direct parent, RootID the main ticket at the top
	// (null for a main ticket itself).
	ParentID *uuid.UUID `gorm:"type:uuid;index" json:"parent_id"`
	RootID   *uuid.UUID `gorm:"type:uuid;index" json:"root_id"`

	WorkflowID     uuid.UUID      `gorm:"type:uuid;not null;index" json:"workflow_id"`
	CurrentStateID uuid.UUID      `gorm:"type:uuid;not null;index" json:"current_state_id"`
	CurrentState   *WorkflowState `gorm:"foreignKey:CurrentStateID" json:"current_state,omitempty"`

	DueDate    *time.Time     `json:"due_date"`
	Attributes datatypes.JSON `gorm:"type:jsonb" json:"attributes"` // the type's own field values

	CreatedByID *uuid.UUID `gorm:"type:uuid" json:"created_by_id"`
	ClosedAt    *time.Time `json:"closed_at"`
	Version     int        `gorm:"not null;default:1" json:"version"` // optimistic lock for moves

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

func (t *Ticket) BeforeCreate(tx *gorm.DB) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	return nil
}

// TicketTransition is one move in a ticket's history.
type TicketTransition struct {
	ID            uuid.UUID  `gorm:"type:uuid;primary_key" json:"id"`
	TicketID      uuid.UUID  `gorm:"type:uuid;not null;index" json:"ticket_id"`
	TransitionID  *uuid.UUID `gorm:"type:uuid" json:"transition_id"`
	FromStateID   uuid.UUID  `gorm:"type:uuid;not null" json:"from_state_id"`
	ToStateID     uuid.UUID  `gorm:"type:uuid;not null" json:"to_state_id"`
	PerformedByID uuid.UUID  `gorm:"type:uuid;not null" json:"performed_by_id"`
	Comment       string     `gorm:"type:text" json:"comment"`
	CreatedAt     time.Time  `gorm:"index" json:"created_at"`
}

func (t *TicketTransition) BeforeCreate(tx *gorm.DB) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	return nil
}

type TicketAssignmentRequest struct {
	Attributes map[string]interface{} `json:"attributes"`
}

// TicketTransitionRequest is the body of POST /tickets/:id/transition.
type TicketTransitionRequest struct {
	TransitionID string `json:"transition_id" validate:"required,uuid"`
	Version      int    `json:"version" validate:"required,min=1"`
	Comment      string `json:"comment" validate:"max=4000"`
}

// TicketHistoryRow is one line of GET /tickets/:id/history.
type TicketHistoryRow struct {
	Transition  string    `json:"transition"`
	FromState   string    `json:"from_state"`
	ToState     string    `json:"to_state"`
	PerformedBy string    `json:"performed_by"`
	Comment     string    `json:"comment"`
	At          time.Time `json:"at"`
}

// TicketListRow is one line of GET /tickets.
type TicketListRow struct {
	ID        uuid.UUID  `json:"id"`
	Reference string     `json:"reference"`
	Title     string     `json:"title"`
	TypeCode  string     `json:"type_code"`
	TypeName  string     `json:"type_name"`
	State     string     `json:"state"`
	StateCode string     `json:"state_code"`
	ParentID  *uuid.UUID `json:"parent_id"`
	RootID    *uuid.UUID `json:"root_id"`
	DueDate   *time.Time `json:"due_date"`
	ClosedAt  *time.Time `json:"closed_at"`
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type TicketFilter struct {
	TypeCodes   []string `json:"type_codes"`   // ["DIR","ASG"]
	Levels      []string `json:"levels"`       // ticket type levels: ["main","universal"]
	TopLevel    bool     `json:"top_level"`    // only tickets with no parent
	HasChildren bool     `json:"has_children"` // only tickets that have child tickets
	ParentID    string   `json:"parent_id"`    // direct children of a ticket
	RootID      string   `json:"root_id"`      // the whole tree under a main ticket
	States      []string `json:"states"`       // state codes: ["registered","planning"]
	Status      string   `json:"status"`       // open | closed
	CreatedBy   string   `json:"created_by"`   // user id
	Search      string   `json:"search"`       // reference or title contains
	DueFrom     string   `json:"due_from"`     // YYYY-MM-DD
	DueTo       string   `json:"due_to"`       // YYYY-MM-DD, inclusive
	CreatedFrom string   `json:"created_from"` // YYYY-MM-DD
	CreatedTo   string   `json:"created_to"`   // YYYY-MM-DD, inclusive
	Overdue     bool     `json:"overdue"`      // open and past due

	// Attributes filters the type's own fields: handle → a value, or a list of
	// values (any of them). Multi-value fields match when they contain the value.
	Attributes map[string]interface{} `json:"attributes"`

	Sort  string `json:"sort"`  // created_at | updated_at | due_date | reference | title
	Order string `json:"order"` // asc | desc (default desc)
	Page  int    `json:"page"`  // default 1
	Limit int    `json:"limit"` // default 20, max 100
}

type TicketActivityFilter struct {
	Action string `query:"action"` // commented,transitioned
	From   string `query:"from"`   // YYYY-MM-DD
	To     string `query:"to"`     // YYYY-MM-DD, inclusive
}

type TicketTypeFilter struct {
	Status string `query:"status"` // draft,live,not_started
	Level  string `query:"level"`  // main,sub,sub_sub,universal
	Search string `query:"search"` // code or name contains
}

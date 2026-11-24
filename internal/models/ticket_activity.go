package models

// Ticket comments, attachments and the activity log.

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// TicketComment is a comment written on a ticket.
type TicketComment struct {
	ID         uuid.UUID      `gorm:"type:uuid;primary_key" json:"id"`
	TicketID   uuid.UUID      `gorm:"type:uuid;not null;index" json:"ticket_id"`
	AuthorID   uuid.UUID      `gorm:"type:uuid;not null" json:"author_id"`
	Content    string         `gorm:"type:text;not null" json:"content"`
	IsInternal bool           `gorm:"default:false" json:"is_internal"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	DeletedAt  gorm.DeletedAt `gorm:"index" json:"-"`
}

func (c *TicketComment) BeforeCreate(tx *gorm.DB) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	return nil
}

// TicketAttachment is a file on a ticket; the file itself is in MinIO.
type TicketAttachment struct {
	ID           uuid.UUID      `gorm:"type:uuid;primary_key" json:"id"`
	TicketID     uuid.UUID      `gorm:"type:uuid;not null;index" json:"ticket_id"`
	FileName     string         `gorm:"size:255;not null" json:"file_name"`
	FileSize     int64          `json:"file_size"`
	MimeType     string         `gorm:"size:100" json:"mime_type"`
	FilePath     string         `gorm:"size:500;not null" json:"-"`
	UploadedByID uuid.UUID      `gorm:"type:uuid;not null" json:"uploaded_by_id"`
	CreatedAt    time.Time      `json:"created_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}

func (a *TicketAttachment) BeforeCreate(tx *gorm.DB) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	return nil
}

// Activity actions.
const (
	TicketActivityCreated           = "created"
	TicketActivityChildCreated      = "child_created"
	TicketActivityTransitioned      = "transitioned"
	TicketActivityCommented         = "commented"
	TicketActivityAttachmentAdded   = "attachment_added"
	TicketActivityAttachmentRemoved = "attachment_removed"
	TicketActivityAssigned          = "assigned"
)

// TicketActivity is one line of a ticket's activity log. Every ticket API
// that changes something writes one.
type TicketActivity struct {
	ID            uuid.UUID      `gorm:"type:uuid;primary_key" json:"id"`
	TicketID      uuid.UUID      `gorm:"type:uuid;not null;index" json:"ticket_id"`
	Action        string         `gorm:"size:50;not null;index" json:"action"`
	Description   string         `gorm:"type:text;not null" json:"description"`
	Details       datatypes.JSON `gorm:"type:jsonb" json:"details"` // e.g. from/to state, file name, comment id
	PerformedByID uuid.UUID      `gorm:"type:uuid;not null" json:"performed_by_id"`
	CreatedAt     time.Time      `gorm:"index" json:"created_at"`
}

func (a *TicketActivity) BeforeCreate(tx *gorm.DB) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	return nil
}

// TicketCommentRequest is the body of POST /tickets/:id/comments.
type TicketCommentRequest struct {
	Content    string `json:"content" validate:"required,max=4000"`
	IsInternal bool   `json:"is_internal"`
}

// TicketCommentRow is one line of GET /tickets/:id/comments.
type TicketCommentRow struct {
	ID         uuid.UUID `json:"id"`
	Content    string    `json:"content"`
	IsInternal bool      `json:"is_internal"`
	Author     string    `json:"author"`
	CreatedAt  time.Time `json:"created_at"`
}

// TicketAttachmentRow is one line of GET /tickets/:id/attachments.
type TicketAttachmentRow struct {
	ID         uuid.UUID `json:"id"`
	FileName   string    `json:"file_name"`
	FileSize   int64     `json:"file_size"`
	MimeType   string    `json:"mime_type"`
	UploadedBy string    `json:"uploaded_by"`
	CreatedAt  time.Time `json:"created_at"`
}

// TicketActivityRow is one line of GET /tickets/:id/activity.
type TicketActivityRow struct {
	Action      string         `json:"action"`
	Description string         `json:"description"`
	Details     datatypes.JSON `json:"details"`
	PerformedBy string         `json:"performed_by"`
	At          time.Time      `json:"at"`
}

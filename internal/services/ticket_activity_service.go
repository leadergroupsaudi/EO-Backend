package services

// Ticket comments, attachments and the activity log.

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

// userName is the SQL for a user's display name (users table aliased u).
const userName = "COALESCE(NULLIF(TRIM(u.first_name || ' ' || u.last_name), ''), u.username, '')"

// logActivity writes one line to the ticket's activity log.
func logActivity(tx *gorm.DB, ticketID uuid.UUID, action, description string, details map[string]interface{}, userID uuid.UUID) error {
	raw, _ := json.Marshal(details)
	return tx.Create(&models.TicketActivity{
		TicketID:      ticketID,
		Action:        action,
		Description:   description,
		Details:       datatypes.JSON(raw),
		PerformedByID: userID,
	}).Error
}

func (s *TicketService) ticketExists(ctx context.Context, id uuid.UUID) (*models.Ticket, error) {
	var t models.Ticket
	if err := s.db.WithContext(ctx).First(&t, "id = ?", id).Error; err != nil {
		return nil, errors.New("ticket not found")
	}
	return &t, nil
}

// Activity returns a ticket's activity log, oldest first.
func (s *TicketService) Activity(ctx context.Context, ticketID uuid.UUID, f *models.TicketActivityFilter) ([]models.TicketActivityRow, error) {
	q := s.db.WithContext(ctx).Table("ticket_activities a").
		Joins("LEFT JOIN users u ON u.id = a.performed_by_id").
		Where("a.ticket_id = ?", ticketID)
	if actions := SplitList(f.Action); len(actions) > 0 {
		q = q.Where("a.action IN ?", actions)
	}
	q, err := dayRange(q, "a.created_at", f.From, f.To, "from", "to")
	if err != nil {
		return nil, err
	}
	rows := []models.TicketActivityRow{}
	err = q.Select("a.action, a.description, a.details, " + userName + " AS performed_by, a.created_at AS at").
		Order("a.created_at").Scan(&rows).Error
	return rows, err
}

// ---------------------------------------------------------------------------
// Comments
// ---------------------------------------------------------------------------

func (s *TicketService) AddComment(ctx context.Context, ticketID uuid.UUID, req *models.TicketCommentRequest, userID uuid.UUID) (*models.TicketComment, error) {
	if _, err := s.ticketExists(ctx, ticketID); err != nil {
		return nil, err
	}
	c := &models.TicketComment{TicketID: ticketID, AuthorID: userID, Content: req.Content, IsInternal: req.IsInternal}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(c).Error; err != nil {
			return err
		}
		return logActivity(tx, ticketID, models.TicketActivityCommented, "Comment added",
			map[string]interface{}{"comment_id": c.ID, "is_internal": c.IsInternal}, userID)
	})
	return c, err
}

// ListComments returns a ticket's comments; isInternal, when set, keeps only
// internal (true) or public (false) ones.
func (s *TicketService) ListComments(ctx context.Context, ticketID uuid.UUID, isInternal *bool) ([]models.TicketCommentRow, error) {
	q := s.db.WithContext(ctx).Table("ticket_comments c").
		Joins("LEFT JOIN users u ON u.id = c.author_id").
		Where("c.ticket_id = ? AND c.deleted_at IS NULL", ticketID)
	if isInternal != nil {
		q = q.Where("c.is_internal = ?", *isInternal)
	}
	rows := []models.TicketCommentRow{}
	err := q.Select("c.id, c.content, c.is_internal, " + userName + " AS author, c.created_at").
		Order("c.created_at").Scan(&rows).Error
	return rows, err
}

// ---------------------------------------------------------------------------
// Attachments (the handler uploads the file to MinIO; this records it)
// ---------------------------------------------------------------------------

func (s *TicketService) AddAttachment(ctx context.Context, a *models.TicketAttachment) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(a).Error; err != nil {
			return err
		}
		return logActivity(tx, a.TicketID, models.TicketActivityAttachmentAdded, "Attachment added: "+a.FileName,
			map[string]interface{}{"attachment_id": a.ID, "file_name": a.FileName, "file_size": a.FileSize}, a.UploadedByID)
	})
}

func (s *TicketService) ListAttachments(ctx context.Context, ticketID uuid.UUID) ([]models.TicketAttachmentRow, error) {
	rows := []models.TicketAttachmentRow{}
	err := s.db.WithContext(ctx).Table("ticket_attachments a").
		Select("a.id, a.file_name, a.file_size, a.mime_type, "+userName+" AS uploaded_by, a.created_at").
		Joins("LEFT JOIN users u ON u.id = a.uploaded_by_id").
		Where("a.ticket_id = ? AND a.deleted_at IS NULL", ticketID).
		Order("a.created_at").Scan(&rows).Error
	return rows, err
}

func (s *TicketService) GetAttachment(ctx context.Context, attachmentID uuid.UUID) (*models.TicketAttachment, error) {
	var a models.TicketAttachment
	if err := s.db.WithContext(ctx).First(&a, "id = ?", attachmentID).Error; err != nil {
		return nil, errors.New("attachment not found")
	}
	return &a, nil
}

// DeleteAttachment removes the record (only its uploader may) and returns it
// so the handler can delete the file from MinIO.
func (s *TicketService) DeleteAttachment(ctx context.Context, attachmentID, userID uuid.UUID) (*models.TicketAttachment, error) {
	a, err := s.GetAttachment(ctx, attachmentID)
	if err != nil {
		return nil, err
	}
	if a.UploadedByID != userID {
		return nil, errors.New("you can only delete your own attachments")
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(a).Error; err != nil {
			return err
		}
		return logActivity(tx, a.TicketID, models.TicketActivityAttachmentRemoved, fmt.Sprintf("Attachment removed: %s", a.FileName),
			map[string]interface{}{"attachment_id": a.ID, "file_name": a.FileName}, userID)
	})
	return a, err
}

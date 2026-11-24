package handlers

// POC — Ticket Type Builder endpoints.

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/services"
	"github.com/automax/backend/internal/storage"
	"github.com/automax/backend/pkg/constants"
	"github.com/automax/backend/pkg/utils"
	"github.com/automax/backend/pkg/validation"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type TicketHandler struct {
	service *services.TicketService
	storage *storage.MinIOStorage
}

func NewTicketHandler(service *services.TicketService, storage *storage.MinIOStorage) *TicketHandler {
	return &TicketHandler{service: service, storage: storage}
}

func typeCode(c *fiber.Ctx) string {
	code := c.Params("code")
	if decoded, err := url.PathUnescape(code); err == nil {
		code = decoded
	}
	return strings.TrimSpace(code)
}

// GET /ticket-types?status=live&level=main&search=dir
func (h *TicketHandler) ListTypes(c *fiber.Ctx) error {
	var f models.TicketTypeFilter
	if err := c.QueryParser(&f); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid query parameters")
	}
	rows, err := h.service.ListTypes(c.UserContext(), &f)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "ok", rows)
}

// GET /ticket-types/:code
func (h *TicketHandler) GetType(c *fiber.Ctx) error {
	d, err := h.service.GetType(c.UserContext(), typeCode(c))
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, err.Error())
	}
	next, _ := h.service.PreviewNextReference(c.UserContext(), typeCode(c))
	return utils.SuccessResponse(c, fiber.StatusOK, "ok", fiber.Map{"type": d.Type, "links": d.Links, "next_reference": next})
}

// POST /ticket-types — create a draft ticket type
func (h *TicketHandler) CreateType(c *fiber.Ctx) error {
	var req models.TicketTypeCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid request body")
	}
	if errs := validation.ValidateStruct(c.UserContext(), &req); len(errs) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"success": false, "errors": errs})
	}
	t, err := h.service.CreateType(c.UserContext(), &req)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	return utils.SuccessResponse(c, fiber.StatusCreated, "ticket type created", t)
}

// PUT /ticket-types/:code — edit a ticket type
func (h *TicketHandler) UpdateType(c *fiber.Ctx) error {
	var req models.TicketTypeUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid request body")
	}
	if errs := validation.ValidateStruct(c.UserContext(), &req); len(errs) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"success": false, "errors": errs})
	}
	t, err := h.service.UpdateType(c.UserContext(), typeCode(c), &req)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "ticket type updated", t)
}

// PUT /ticket-types/:code/fields — replace the field list
func (h *TicketHandler) SetFields(c *fiber.Ctx) error {
	var req models.TicketTypeFieldsRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid request body")
	}
	if errs := validation.ValidateStruct(c.UserContext(), &req); len(errs) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"success": false, "errors": errs})
	}
	t, err := h.service.SetFields(c.UserContext(), typeCode(c), req.Fields)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "fields saved", t)
}

// POST /ticket-types/:code/publish — make a draft type live
func (h *TicketHandler) PublishType(c *fiber.Ctx) error {
	t, checks, err := h.service.PublishType(c.UserContext(), typeCode(c))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"success": false, "error": err.Error(), "checks": checks})
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "ticket type is live", fiber.Map{"type": t, "checks": checks})
}

// DELETE /ticket-types/:code — delete a type that is not live and has no tickets
func (h *TicketHandler) DeleteType(c *fiber.Ctx) error {
	if err := h.service.DeleteType(c.UserContext(), typeCode(c)); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "ticket type deleted", nil)
}

// PUT /ticket-types/:code/links
func (h *TicketHandler) SaveLink(c *fiber.Ctx) error {
	var req models.TicketTypeLinkRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid request body")
	}
	if errs := validation.ValidateStruct(c.UserContext(), &req); len(errs) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"success": false, "errors": errs})
	}
	link, err := h.service.SaveLink(c.UserContext(), typeCode(c), &req)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "link saved", link)
}

// POST /tickets
func (h *TicketHandler) CreateTicket(c *fiber.Ctx) error {
	var req models.TicketCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid request body")
	}
	if errs := validation.ValidateStruct(c.UserContext(), &req); len(errs) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"success": false, "errors": errs})
	}
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	inc, err := h.service.CreateTicket(c.UserContext(), &req, userID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	return utils.SuccessResponse(c, fiber.StatusCreated, "ticket created", fiber.Map{"id": inc.ID, "reference": inc.Reference})
}

// GET /tickets/:id
func (h *TicketHandler) GetTicket(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid id")
	}
	d, err := h.service.GetTicket(c.UserContext(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, err.Error())
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "ok", d)
}

// GET /tickets — the filter as query parameters: lists comma-separated
// (type_code=DIR,ASG), attributes as attr.<handle>=<value>.
func (h *TicketHandler) ListTickets(c *fiber.Ctx) error {
	list := func(key string) []string { return services.SplitList(c.Query(key)) }
	f := models.TicketFilter{
		TypeCodes:   list("type_code"),
		Levels:      list("level"),
		TopLevel:    c.QueryBool("top_level"),
		HasChildren: c.QueryBool("has_children"),
		ParentID:    c.Query("parent_id"),
		RootID:      c.Query("root_id"),
		States:      list("state"),
		Status:      c.Query("status"),
		CreatedBy:   c.Query("created_by"),
		Search:      c.Query("search"),
		DueFrom:     c.Query("due_from"),
		DueTo:       c.Query("due_to"),
		CreatedFrom: c.Query("created_from"),
		CreatedTo:   c.Query("created_to"),
		Overdue:     c.QueryBool("overdue"),
		Sort:        c.Query("sort"),
		Order:       c.Query("order"),
		Page:        c.QueryInt("page"),
		Limit:       c.QueryInt("limit"),
		Attributes:  map[string]interface{}{},
	}
	for k, v := range c.Queries() {
		if strings.HasPrefix(k, "attr.") {
			f.Attributes[strings.TrimPrefix(k, "attr.")] = v
		}
	}
	return h.listTickets(c, &f)
}

// POST /tickets/search — the same filter as a JSON body (models.TicketFilter),
// for filters too big for a query string.
func (h *TicketHandler) SearchTickets(c *fiber.Ctx) error {
	var f models.TicketFilter
	if len(c.Body()) > 0 {
		if err := c.BodyParser(&f); err != nil {
			return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid request body")
		}
	}
	return h.listTickets(c, &f)
}

func (h *TicketHandler) listTickets(c *fiber.Ctx, f *models.TicketFilter) error {
	rows, total, err := h.service.ListTickets(c.UserContext(), f)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	return utils.PaginatedSuccessResponse(c, rows, f.Page, f.Limit, total)
}

// POST /tickets/:id/transition
func (h *TicketHandler) Transition(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid id")
	}
	var req models.TicketTransitionRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid request body")
	}
	if errs := validation.ValidateStruct(c.UserContext(), &req); len(errs) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"success": false, "errors": errs})
	}
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	d, err := h.service.Transition(c.UserContext(), id, &req, userID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "transition done", d)
}

// PUT /tickets/:id/assignment — set the owning body, owner, priority, due date
// and what is expected back (all required)
func (h *TicketHandler) Assign(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid id")
	}
	var req models.TicketAssignmentRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid request body")
	}
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	d, err := h.service.Assign(c.UserContext(), id, &req, userID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "assignment saved", d)
}

// GET /tickets/:id/history
func (h *TicketHandler) History(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid id")
	}
	rows, err := h.service.History(c.UserContext(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "ok", rows)
}

// GET /tickets/:id/activity?action=commented,transitioned&from=2026-11-01&to=2026-11-30
func (h *TicketHandler) Activity(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid id")
	}
	var f models.TicketActivityFilter
	if err := c.QueryParser(&f); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid query parameters")
	}
	rows, err := h.service.Activity(c.UserContext(), id, &f)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "ok", rows)
}

// POST /tickets/:id/comments
func (h *TicketHandler) AddComment(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid id")
	}
	var req models.TicketCommentRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid request body")
	}
	if errs := validation.ValidateStruct(c.UserContext(), &req); len(errs) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"success": false, "errors": errs})
	}
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	cm, err := h.service.AddComment(c.UserContext(), id, &req, userID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	return utils.SuccessResponse(c, fiber.StatusCreated, "comment added", cm)
}

// GET /tickets/:id/comments?is_internal=true
func (h *TicketHandler) ListComments(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid id")
	}
	var isInternal *bool
	switch c.Query("is_internal") {
	case "":
	case "true":
		v := true
		isInternal = &v
	case "false":
		v := false
		isInternal = &v
	default:
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "is_internal must be true or false")
	}
	rows, err := h.service.ListComments(c.UserContext(), id, isInternal)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "ok", rows)
}

// POST /tickets/:id/attachments (multipart, field "file")
func (h *TicketHandler) UploadAttachment(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid id")
	}
	if _, err := h.service.GetTicket(c.UserContext(), id); err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "ticket not found")
	}
	file, err := c.FormFile("file")
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "no file uploaded (form field \"file\")")
	}
	src, err := file.Open()
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "failed to read file")
	}
	defer src.Close()

	path, err := h.storage.UploadFile(c.UserContext(), src, file, fmt.Sprintf("tickets/%s", id))
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "failed to upload file")
	}
	a := &models.TicketAttachment{
		TicketID:     id,
		FileName:     file.Filename,
		FileSize:     file.Size,
		MimeType:     file.Header.Get("Content-Type"),
		FilePath:     path,
		UploadedByID: c.Locals(constants.ContextKeys.UserID).(uuid.UUID),
	}
	if err := h.service.AddAttachment(c.UserContext(), a); err != nil {
		_ = h.storage.DeleteFile(c.UserContext(), path)
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}
	return utils.SuccessResponse(c, fiber.StatusCreated, "attachment uploaded", a)
}

// GET /tickets/:id/attachments
func (h *TicketHandler) ListAttachments(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid id")
	}
	rows, err := h.service.ListAttachments(c.UserContext(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "ok", rows)
}

// GET /tickets/attachments/:attachment_id/download
func (h *TicketHandler) DownloadAttachment(c *fiber.Ctx) error {
	aid, err := uuid.Parse(c.Params("attachment_id"))
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid attachment id")
	}
	a, err := h.service.GetAttachment(c.UserContext(), aid)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, err.Error())
	}
	file, err := h.storage.GetFile(c.UserContext(), a.FilePath)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "failed to read file")
	}
	c.Set("Content-Type", a.MimeType)
	c.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", a.FileName))
	return c.SendStream(file)
}

// DELETE /tickets/attachments/:attachment_id
func (h *TicketHandler) DeleteAttachment(c *fiber.Ctx) error {
	aid, err := uuid.Parse(c.Params("attachment_id"))
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid attachment id")
	}
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	a, err := h.service.DeleteAttachment(c.UserContext(), aid, userID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	_ = h.storage.DeleteFile(c.UserContext(), a.FilePath)
	return utils.SuccessResponse(c, fiber.StatusOK, "attachment deleted", nil)
}

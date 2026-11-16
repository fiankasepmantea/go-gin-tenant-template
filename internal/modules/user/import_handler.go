package user

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/response"
)

const (
	xlsxType        = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	multipartMargin = 64 << 10 // room for the multipart framing around the file
	memoryLimit     = 1 << 20  // what a multipart form keeps in memory; the rest goes to a temporary file
)

// importTemplate serves the .xlsx template.
func (h *Handler) importTemplate(c *gin.Context) {
	data, err := Template()
	if err != nil {
		response.Fail(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="users-template.xlsx"`)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, xlsxType, data)
}

// importUsers creates the users of an uploaded .xlsx file.
func (h *Handler) importUsers(c *gin.Context) {
	dry, err := parseDryRun(c.Query("dry_run"), c.PostForm("dry_run"))
	if err != nil {
		response.Fail(c, importProblem("dry_run", "format", "dry_run must be true or false."))
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxImportBytes+multipartMargin)
	if err := c.Request.ParseMultipartForm(memoryLimit); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			response.Fail(c, err)
			return
		}
		response.Abort(c, response.CodeInvalidRequest, "Send the file as multipart/form-data.")
		return
	}
	defer func() { _ = c.Request.MultipartForm.RemoveAll() }()

	fh, err := c.FormFile("file")
	if err != nil {
		response.Fail(c, importProblem("file", "required", "The file field is required."))
		return
	}
	if fh.Size > MaxImportBytes {
		response.Fail(c, &http.MaxBytesError{Limit: MaxImportBytes})
		return
	}
	src, err := fh.Open()
	if err != nil {
		response.Fail(c, err)
		return
	}
	defer func() { _ = src.Close() }()
	data, err := io.ReadAll(io.LimitReader(src, MaxImportBytes+1))
	if err != nil {
		response.Fail(c, err)
		return
	}

	res, err := h.imp.Import(c.Request.Context(), data, dry)
	if err != nil {
		h.failImport(c, err)
		return
	}
	response.OK(c, res)
}

// parseDryRun reads the flag from the query or the form; empty means false.
func parseDryRun(vals ...string) (bool, error) {
	for _, v := range vals {
		if v != "" {
			return strconv.ParseBool(v)
		}
	}
	return false, nil
}

func importProblem(field, rule, msg string) *response.Error {
	return &response.Error{
		Code: response.CodeValidationFailed, Message: msg,
		Details: []response.FieldError{{Field: field, Rule: rule}},
	}
}

// failImport turns the errors of an import into responses.
func (h *Handler) failImport(c *gin.Context, err error) {
	var (
		ie *ImportError
		mc *MissingColumnsError
	)
	switch {
	case errors.As(err, &ie):
		details := make([]response.FieldError, len(ie.Errors))
		for i, e := range ie.Errors {
			details[i] = response.FieldError{Field: "row " + strconv.Itoa(e.Line) + " " + e.Field, Rule: e.Rule}
		}
		msg := "The file has " + strconv.Itoa(ie.Total) + " problems. Nothing was imported."
		response.Fail(c, &response.Error{Code: response.CodeValidationFailed, Message: msg, Details: details})
	case errors.As(err, &mc):
		response.Fail(c, importProblem("header", "missing", "Missing columns in the first row: "+strings.Join(mc.Columns, ", ")+"."))
	case errors.Is(err, ErrNotSpreadsheet):
		response.Fail(c, importProblem("file", "format", "The file is not a valid .xlsx workbook."))
	case errors.Is(err, ErrNoRows):
		response.Fail(c, importProblem("file", "empty", "The sheet has no users below the header row."))
	case errors.Is(err, ErrTooManyRows):
		response.Fail(c, importProblem("file", "max", "A file can have at most "+strconv.Itoa(MaxImportRows)+" users."))
	default:
		h.fail(c, err)
	}
}

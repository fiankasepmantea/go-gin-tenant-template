package request

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/response"
)

// ListPolicy says what a client may ask of one resource. Everything not listed is rejected, so a typo in a
// filter can never silently return unfiltered data and a client can never sort or filter on a column the
// resource did not expose.
type ListPolicy struct {
	Filterable []string // columns usable as ?column=value (equality)
	Sortable   []string // columns usable in ?sort=
}

// reserved query parameters of every list.
var reserved = []string{"page", "per_page", "sort", "from", "to"}

// ParseList turns the query string into core.ListParams:
//
//	?page=2&per_page=50        pagination (per_page is capped at 100 by the repository)
//	?sort=-created_at,name     a leading "-" means descending
//	?from=2026-01-01&to=...    created_at range, "to" inclusive; a date (YYYY-MM-DD) or an RFC 3339 timestamp
//	?<column>=<value>          equality filter on a Filterable column
//
// The returned error is a *response.Error, ready for response.Fail.
func ParseList(c *gin.Context, p ListPolicy) (core.ListParams, error) {
	var out core.ListParams
	// Not URL.Query(): that silently drops a parameter whose text contains ";", which would turn a broken
	// filter into "no filter at all". ParseQuery reports it instead.
	q, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return out, bad("The query string is malformed.")
	}

	for key, values := range q {
		if len(values) != 1 {
			return out, bad("Query parameter %q must be given once.", key)
		}
		if slices.Contains(reserved, key) {
			continue
		}
		if !slices.Contains(p.Filterable, key) {
			return out, bad("Unknown query parameter %q.", key)
		}
		out.Filters = append(out.Filters, core.Filter{Column: key, Value: values[0]})
	}
	// Map iteration order is random; keep the generated SQL stable.
	slices.SortFunc(out.Filters, func(a, b core.Filter) int { return strings.Compare(a.Column, b.Column) })

	if out.Page, err = intParam(q.Get("page"), "page"); err != nil {
		return out, err
	}
	if out.PerPage, err = intParam(q.Get("per_page"), "per_page"); err != nil {
		return out, err
	}

	if s := q.Get("sort"); s != "" {
		for _, field := range strings.Split(s, ",") {
			desc := strings.HasPrefix(field, "-")
			col := strings.TrimPrefix(field, "-")
			if !slices.Contains(p.Sortable, col) {
				return out, bad("Cannot sort by %q.", col)
			}
			out.Sort = append(out.Sort, core.Sort{Column: col, Desc: desc})
		}
	}

	if s := q.Get("from"); s != "" {
		t, _, err := parseTime(s)
		if err != nil {
			return out, bad("Parameter \"from\" must be a date (YYYY-MM-DD) or an RFC 3339 timestamp.")
		}
		out.CreatedFrom = t
	}
	if s := q.Get("to"); s != "" {
		t, dateOnly, err := parseTime(s)
		if err != nil {
			return out, bad("Parameter \"to\" must be a date (YYYY-MM-DD) or an RFC 3339 timestamp.")
		}
		// core uses an exclusive upper bound: include the whole day, or the exact instant.
		if dateOnly {
			t = t.AddDate(0, 0, 1)
		} else {
			t = t.Add(time.Microsecond) // PostgreSQL timestamps have microsecond precision
		}
		out.CreatedBefore = t
	}
	if !out.CreatedFrom.IsZero() && !out.CreatedBefore.IsZero() && !out.CreatedFrom.Before(out.CreatedBefore) {
		return out, bad("Parameter \"from\" must be before \"to\".")
	}
	return out, nil
}

func intParam(s, name string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, bad("Parameter %q must be a positive integer.", name)
	}
	return n, nil
}

func parseTime(s string) (t time.Time, dateOnly bool, err error) {
	if t, err = time.Parse(time.DateOnly, s); err == nil {
		return t.UTC(), true, nil
	}
	if t, err = time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), false, nil
	}
	return time.Time{}, false, err
}

func bad(format string, args ...any) *response.Error {
	return &response.Error{Code: response.CodeInvalidRequest, Message: fmt.Sprintf(format, args...)}
}

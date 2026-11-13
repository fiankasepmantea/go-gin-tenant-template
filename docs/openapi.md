# OpenAPI / Swagger

The API describes itself. The document is built **from the code** when the server starts: the routes that are
registered, and the Go types the handlers read and write (the `json` and `binding` tags become property names,
`required`, `minLength`, `maxLength`, `minimum`, `maximum`, `enum`, `format`). It cannot drift from the API, and
nothing is written by hand.

| URL | What |
|---|---|
| `GET /openapi.json` | the OpenAPI 3.0.3 document (use it with any generator or client tool) |
| `GET /docs` | Swagger UI: click *Authorize*, paste the `access_token` of `/auth/login`, try the calls |

`/docs` loads Swagger UI from unpkg (a pinned version), so the machine of the reader needs internet access; the
server does not.

## Switch

`DOCS_ENABLED` is `true` in development and `false` in production (the description of an API is for its
developers). Set it to `true` in production only when the route is protected by something in front of it.

## What each operation shows

- method, path with `{id}` parameters, a tag per resource, a summary;
- the request body schema and the success schema, wrapped in the envelope (`success`, `code`, `message`, `data`;
  a list is a `data` with `total_data`, `curr_page`, ..., `data`);
- the query parameters of a list (`page`, `per_page`, `sort`, `from`, `to`, and one per filterable column);
- the error responses that can happen (`ErrorBody`: `error.code`, `message`, `details[].field` in JSON names);
- security: bearer token everywhere except `/auth/login`, `/auth/refresh` and the other public `/auth` routes.

## A new module

`tenantctl make:module` writes `Document` for you (it calls `crud.Handler.Document`), so a generated module is in
the document as soon as it is registered. For a handler written by hand, implement `openapi.Documenter`:

```go
func (h *Handler) Document(d *openapi.Doc) {
	d.Add(openapi.Op{Method: "POST", Path: "/api/v1/orders/:id/pay", Summary: "Pay an order",
		Tag: "orders", Body: payInput{}, Result: Order{}})
}
```

A route that is not described still appears, with its method, path and the common errors.

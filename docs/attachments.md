# Attachments: files for the records of other modules

A pattern for "many photos or documents that belong to one record": photos of an activity, documents of a case,
attachments of a report. Files are uploaded through `/files` (see `docs/files.md`); the attachment table only
stores the **`file_id`**, never a path.

## Flow

1. `POST /api/v1/files` (multipart, field `file`) -> the response contains a `file_id`.
2. `POST /api/v1/activities/:id/documents` with `{"file_id": "..."}` -> the file is attached.
3. `GET /api/v1/activities/:id/documents` -> the attachments, each with `name`, `content_type`, `size`, and `url`
   (the content is fetched from `GET /api/v1/files/<file_id>/content`).
4. `DELETE /api/v1/activities/:id/documents/:attachment_id` -> the attachment row, the file, and its content are deleted.

## Wiring it to your table (example: `activity_documents`)

**1. Migration** (`tenantctl make:migration add_activity_documents`). The parent table (`activities`) needs
`UNIQUE (tenant_id, activity_id)`; `tenantctl make:module` already adds it.

```sql
CREATE TABLE activity_documents (
    activity_document_id uuid        PRIMARY KEY,
    tenant_id            uuid        NOT NULL,
    activity_id          uuid        NOT NULL,
    file_id              uuid        NOT NULL,
    created_at           timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, activity_id) REFERENCES activities (tenant_id, activity_id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, file_id)     REFERENCES files (tenant_id, file_id),
    UNIQUE (file_id)                                    -- a file belongs to one attachment only
);
CREATE INDEX activity_documents_activity_idx ON activity_documents (tenant_id, activity_id);

ALTER TABLE activity_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE activity_documents FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON activity_documents
    USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
```

**2. Register the handler** in `internal/app/app.go`, above the `// tenantctl:modules` line:

```go
attachment.NewHandler(attachment.Config{
    Name: "documents", Parent: "activities",
    Table: "activity_documents", IDColumn: "activity_document_id",
    ParentTable: "activities", ParentColumn: "activity_id",
}, d.DB, file.NewRepository(d.DB), d.Storage, chain...),
```

For a second table repeat this with other table and column names. Then run `endpoint sync` and `group grant`.

## Why the foreign keys are pairs `(tenant_id, ...)`

Foreign key checks in PostgreSQL are not subject to RLS. With a plain foreign key (`activity_id` alone), tenant B
could guess the id of an activity of tenant A and attach a file to it. A composite foreign key makes the database
itself refuse that. The application checks as well (404 for a record of another tenant, 422 for a file of another
tenant), so there are two layers.

## Notes

- Table and column names in `Config` are put into SQL, so only lowercase letters, digits and `_` are accepted
  (anything else stops the handler when the server starts). The values come from code, never from a request.
- Deleting the parent record deletes the attachment rows (`ON DELETE CASCADE`) but **not** the files and their
  content. Detach through the `DELETE` endpoint first, or write the cleanup in the parent module.
- The list of attachments is limited by `Limit` (default 200) per record.

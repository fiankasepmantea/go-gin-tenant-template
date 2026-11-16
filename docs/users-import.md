# Importing users from Excel

An administrator can create many users of the own tenant from one `.xlsx` file.

| Route | What it does |
| --- | --- |
| `GET /api/v1/users/import/template` | Downloads `users-template.xlsx` (header row and a Help sheet) |
| `POST /api/v1/users/import` | Imports the file (multipart/form-data, field `file`) |
| `POST /api/v1/users/import?dry_run=true` | Checks the file and reports every problem, creates nothing |

Both routes are normal tenant endpoints: run `tenantctl endpoint sync`, then grant them to the group that may
import (a group with `--all` already has them).

## The sheet

The first sheet is read. Its first row names the columns, in any order (upper or lower case, extra columns ignored).

| Column | Required | Rule |
| --- | --- | --- |
| `username` | yes | 3 to 64 characters, unique in the tenant (case-insensitive) |
| `email` | yes | a plain address like `a@b.test`, unique in the tenant (case-insensitive) |
| `name` | yes | up to 100 characters |
| `password` | yes | the password policy (at least 10 characters); stored as a hash |
| `group` | no | the **name** of an existing group of the tenant; empty means no group |
| `is_active` | no | `yes`/`no` (also `true`/`false`, `1`/`0`); empty means yes |

Completely empty rows are skipped. Cells are read as typed, so a password or username made of digits stays as it is.

## All or nothing

Every row is checked before anything is created: format, the password policy, duplicates inside the file,
users that already exist in the tenant, and unknown groups. If any row has a problem the answer is 422 and
**no user is created**. The answer lists the problems (at most 100, the message gives the total):

```json
{ "success": false, "code": 422,
  "error": { "code": "validation_failed", "message": "The file has 2 problems. Nothing was imported.",
    "details": [ { "field": "row 3 password", "rule": "min" }, { "field": "row 5 group", "rule": "unknown" } ] } }
```

`row N` is the row number in the spreadsheet (the header is row 1). Rules: `required`, `min`, `max`, `format`,
`duplicate` (the second of two equal rows), `taken` (already a user), `unknown` (no such group).

On success: `{"data": {"rows": 120, "created": 120, "dry_run": false}}`. Creation runs inside the request's
transaction, so a failure halfway also creates nothing.

## Limits

- File size: 5 MiB (413 above). At most 500 users per file. Larger imports: split the file.
- The file is read by `internal/platform/xlsx`, which uses only the Go standard library: formulas are never evaluated, entities are not expanded, and limits apply to the unpacked size, rows and columns, so a zip bomb or a huge sheet is refused. It reads text, numbers and booleans of the first sheet; an encrypted workbook is refused.
- Hashing a password is slow on purpose, so a full file of 500 users takes a few seconds.
- Passwords are in the file in clear text: tell people to delete it after the import.

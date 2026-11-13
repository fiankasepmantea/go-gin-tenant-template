-- Lets other tables reference a file with a composite foreign key (tenant_id, file_id), so a row of one tenant
-- can never point to a file of another tenant (docs/attachments.md).
ALTER TABLE files ADD CONSTRAINT files_tenant_file_uq UNIQUE (tenant_id, file_id);

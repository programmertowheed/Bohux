-- +goose Up
-- EMAIL notification channels (ADR-0043). Their recipients are a mailto: URL kept in
-- OpenBao like every channel URL; only the allowed kinds change here.
ALTER TABLE notification_channels_heads DROP CONSTRAINT notification_channels_heads_kind_check;
ALTER TABLE notification_channels_heads ADD CONSTRAINT notification_channels_heads_kind_check
    CHECK (kind IN ('slack', 'discord', 'webhook', 'email'));

-- +goose Down
-- Reversible while no EMAIL channel exists; delete those first, or this fails.
ALTER TABLE notification_channels_heads DROP CONSTRAINT notification_channels_heads_kind_check;
ALTER TABLE notification_channels_heads ADD CONSTRAINT notification_channels_heads_kind_check
    CHECK (kind IN ('slack', 'discord', 'webhook'));

-- The sweepers read no tenant's secrets.
--
-- dude_sweeper bypasses row-level security to find work across
-- organizations (webhook deliveries to process, pull requests to re-read),
-- and then does that work inside one organization's scope. It never needs
-- a GitHub token or a webhook secret, yet 014 granted it forge_credentials
-- and 045 repeated the grant. Every read of the table goes through one
-- organization (the forge resolver, the GitHub settings routes).
REVOKE SELECT ON forge_credentials FROM dude_sweeper;

-- The dude layers an installed release built and pushed. Nobody's data,
-- like image_builder: dude_app reads them, only dude_builder writes.
-- The most recently selected layer is current; selecting a recorded tag
-- again (a rollback) changes no image in the registry.
CREATE TABLE image_layers (
  tag        text PRIMARY KEY,
  ref        text NOT NULL CHECK (ref ~ '@sha256:[0-9a-f]{64}$'),
  platform   text NOT NULL,
  version    text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  current_at timestamptz NOT NULL DEFAULT now()
);
GRANT SELECT ON image_layers TO dude_app;
GRANT SELECT, INSERT, UPDATE ON image_layers TO dude_builder;

-- Whether a server links the shared mods (<root>/shared/Mods) into its Mods
-- folder: 0 no, 1 all of them, 2 too but ones shared later arrive disabled.
ALTER TABLE instances ADD COLUMN shared_mods INTEGER NOT NULL DEFAULT 0;

-- Нативная квота отдельного API-предложения. Старые наблюдения остаются NULL.
-- HTML-признаки Bnovo сохраняют собственные поля и семантику.
ALTER TABLE {{schema}}.observation_offers
 ADD COLUMN reported_quantity bigint CHECK (reported_quantity >= 0);

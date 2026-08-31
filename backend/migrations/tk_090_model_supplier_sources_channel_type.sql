-- TokenKey: explicit Extension Engine channel_type on supplier sources for transport + model discovery.

ALTER TABLE model_supplier_sources
    ADD COLUMN IF NOT EXISTS channel_type INTEGER NOT NULL DEFAULT 1;

UPDATE model_supplier_sources
SET channel_type = 46
WHERE endpoint ILIKE '%qianfan.baidubce.com%';

UPDATE model_supplier_sources
SET channel_type = 17
WHERE endpoint ILIKE '%dashscope.aliyuncs.com%';

ALTER TABLE model_supplier_sources
    ADD CONSTRAINT model_supplier_sources_channel_type_check
        CHECK (channel_type > 0);
